#!/usr/bin/env python3
"""Phase 2 chip-threshold calibration — picks the cutoff that maps
binary-chip `predicted_value` (probability/event_base_rate) to a
user-visible `elevated` vs `ok` state.

Scope contract (frozen before run):

- Two targets:
    * `acute_risk / event_t1_t3`
    * `chronic_load / chronic_label`
  `chronic_load / chronic_acute_density` stays silent (operator
  decision documented in plan §6.1).
- Tenants: defaults to `health`; pass `--schema=<name>` for others.
- Data slice: `source_2025_current` epoch (same as Phase 1 floors
  and feasibility work).
- Join: `naive_baselines` rows where `predicted_value IS NOT NULL`
  with their sibling `target_snapshots` row where `eligible=TRUE`
  and `target_value` populated. Pairs (predicted_value, label).
- Output: markdown report with one section per target —
  base rate, distribution quartiles, ROC sweep, precision-recall
  sweep, and three candidate cutoff rules with their per-rule
  precision / recall / elevated-rate. Operator picks one in the
  follow-up Go config PR.

Three candidate rules computed for each target:

  A. Top-15% rule — chip shows `elevated` when `predicted_value`
     >= 85th percentile of the in-slice distribution. Tenant-
     adaptive; rebuilds when the underlying distribution shifts.
  B. Fixed cutoff at base-rate × 1.5 — chip shows `elevated` when
     `predicted_value >= 1.5 * base_rate`. Tenant-adaptive in the
     same way base rate already is. Simple to explain.
  C. Max-F1 cutoff — threshold that maximises F1 on the slice.
     Mathematical sweet spot; not necessarily the operationally
     useful one but reports the upper bound on what threshold
     choice can deliver.

Insufficient-data rule (stamped in the report, not enforced here):
  If fewer than 30 eligible (predicted_value, label) pairs exist in
  the slice, the chip stays unknown regardless of value. Documented
  alongside the cutoffs so the Go config PR can codify it.

Deps: numpy only (matches the linear feasibility scripts).

## Running

    PSQL=/path/to/psql.exe PYTHONUTF8=1 python \\
        analysis/phase8_chip_threshold_calibration/calibration.py \\
        > READINESS_REDESIGN_PHASE2_CHIP_THRESHOLD_CALIBRATION.md

Uses libpq env vars (PGHOST/PGUSER/PGDATABASE/PGPASSWORD/PGOPTIONS).
"""

from __future__ import annotations

import math
import os
import subprocess
import sys
from dataclasses import dataclass
from datetime import date, datetime

import numpy as np


PSQL = os.environ.get("PSQL", "psql")
SCHEMA = os.environ.get("PGOPTIONS_SCHEMA", "health")
TEST_START = date(2025, 1, 1)
TEST_END = date(2026, 5, 17)
MIN_PAIRS = 30  # insufficient-data threshold

TARGETS = [
    ("acute_risk", "event_t1_t3"),
    ("chronic_load", "chronic_label"),
]


@dataclass
class Pair:
    date: date
    predicted: float
    label: int


def psql(sql: str) -> list[list[str]]:
    result = subprocess.run(
        [PSQL, "-X", "-A", "-F", "\t", "-t", "-c", sql],
        check=True, capture_output=True, text=True,
    )
    out = []
    for line in result.stdout.splitlines():
        if not line:
            continue
        out.append([c if c != "" else None for c in line.split("\t")])
    return out


def load_pairs(sub_score: str, target_kind: str) -> list[Pair]:
    """Join naive_baselines with target_snapshots over the test slice.

    Both sides must be eligible / populated. `target_snapshots` is the
    observed forward-window label; `naive_baselines.predicted_value`
    is the deployable prediction the chip would render. Joining them
    gives ground truth for the threshold sweep.
    """
    sql = f"""
        SELECT n.date, n.predicted_value, t.target_value
          FROM naive_baselines n
          JOIN target_snapshots t
            ON t.date = n.date AND t.sub_score = n.sub_score
               AND t.target_kind = n.target_kind
         WHERE n.sub_score = '{sub_score}'
           AND n.target_kind = '{target_kind}'
           AND n.baseline_kind = 'event_base_rate'
           AND n.predicted_value IS NOT NULL
           AND t.eligible = TRUE
           AND t.target_value IS NOT NULL
           AND n.source_epoch = 'source_2025_current'
           AND n.date BETWEEN '{TEST_START}' AND '{TEST_END}'
         ORDER BY n.date ASC
    """
    pairs = []
    for r in psql(sql):
        d = datetime.strptime(r[0], "%Y-%m-%d").date()
        pred = float(r[1])
        label = 1 if float(r[2]) >= 0.5 else 0
        pairs.append(Pair(d, pred, label))
    return pairs


def sweep_thresholds(pairs: list[Pair], thresholds: list[float]):
    """For each threshold, return (threshold, elevated_rate, precision, recall, f1)."""
    ys = np.array([p.label for p in pairs], dtype=np.int64)
    preds = np.array([p.predicted for p in pairs], dtype=np.float64)
    n = len(pairs)
    base_rate = float(ys.mean()) if n else math.nan
    rows = []
    for thr in thresholds:
        flagged = preds >= thr
        n_flag = int(flagged.sum())
        elevated_rate = n_flag / n if n else math.nan
        if n_flag == 0:
            precision = math.nan
        else:
            precision = float(ys[flagged].sum()) / n_flag
        n_pos = int(ys.sum())
        if n_pos == 0:
            recall = math.nan
        else:
            recall = float(ys[flagged].sum()) / n_pos
        if (precision != precision) or (recall != recall) or (precision + recall == 0):
            f1 = math.nan
        else:
            f1 = 2 * precision * recall / (precision + recall)
        rows.append((thr, elevated_rate, precision, recall, f1, n_flag))
    return rows, base_rate


def candidate_rules(pairs: list[Pair]) -> dict:
    """Compute the three pre-declared cutoff rules + their metrics."""
    if not pairs:
        return {"error": "no pairs"}
    preds = np.array([p.predicted for p in pairs], dtype=np.float64)
    ys = np.array([p.label for p in pairs], dtype=np.int64)
    base_rate = float(ys.mean())

    # Rule A — top-15% (>=85th percentile).
    p85 = float(np.percentile(preds, 85))

    # Rule B — base_rate * 1.5. May be inside or outside the
    # observed distribution range — clamp evaluation either way.
    rule_b = base_rate * 1.5

    # Rule C — max-F1 sweep over the unique predicted values.
    uniq = sorted(set(preds.tolist()))
    best_thr, best_f1 = uniq[0], -1.0
    for thr in uniq:
        flagged = preds >= thr
        if not flagged.any():
            continue
        precision = float(ys[flagged].sum()) / int(flagged.sum())
        n_pos = int(ys.sum())
        if n_pos == 0:
            continue
        recall = float(ys[flagged].sum()) / n_pos
        if precision + recall == 0:
            continue
        f1 = 2 * precision * recall / (precision + recall)
        if f1 > best_f1:
            best_f1, best_thr = f1, thr

    sweep, _ = sweep_thresholds(pairs, [p85, rule_b, best_thr])
    return {
        "base_rate": base_rate,
        "rule_a": ("top_15_pct (p85)", p85, sweep[0]),
        "rule_b": ("base_rate * 1.5", rule_b, sweep[1]),
        "rule_c": ("max_f1", best_thr, sweep[2]),
    }


def fmt(x, p=4):
    if x is None or (isinstance(x, float) and (math.isnan(x) or math.isinf(x))):
        return "—"
    return f"{x:.{p}f}"


def render_target(sub_score: str, target_kind: str, pairs: list[Pair]) -> str:
    out = []
    out.append(f"## `{sub_score} / {target_kind}`")
    out.append("")
    out.append(f"- Eligible (predicted, label) pairs in slice: **{len(pairs)}**")
    if len(pairs) < MIN_PAIRS:
        out.append(f"- **Insufficient data** (fewer than {MIN_PAIRS} pairs). "
                   f"Chip should stay `unknown` regardless of threshold. "
                   f"Re-run once more eligible rows accumulate.")
        out.append("")
        return "\n".join(out)

    preds = np.array([p.predicted for p in pairs], dtype=np.float64)
    ys = np.array([p.label for p in pairs], dtype=np.int64)
    base_rate = float(ys.mean())
    out.append(f"- Positive rate (label = 1): **{fmt(base_rate, 3)}** "
               f"({int(ys.sum())} of {len(pairs)})")
    out.append(f"- `predicted_value` distribution quartiles: "
               f"min={fmt(float(preds.min()), 3)}, "
               f"p25={fmt(float(np.percentile(preds, 25)), 3)}, "
               f"p50={fmt(float(np.median(preds)), 3)}, "
               f"p75={fmt(float(np.percentile(preds, 75)), 3)}, "
               f"p85={fmt(float(np.percentile(preds, 85)), 3)}, "
               f"p95={fmt(float(np.percentile(preds, 95)), 3)}, "
               f"max={fmt(float(preds.max()), 3)}")
    out.append("")

    # Threshold sweep at distribution-derived cuts.
    p25 = float(np.percentile(preds, 25))
    p50 = float(np.median(preds))
    p75 = float(np.percentile(preds, 75))
    p85 = float(np.percentile(preds, 85))
    p95 = float(np.percentile(preds, 95))
    sweep_thr = [p25, p50, p75, p85, p95]
    sweep, _ = sweep_thresholds(pairs, sweep_thr)

    out.append("### Threshold sweep")
    out.append("")
    out.append("| cut | threshold | elevated_rate | precision | recall | F1 | n_flagged |")
    out.append("|---|---|---|---|---|---|---|")
    for (thr, er, p, rec, f1, nf), label in zip(
        sweep, ["p25", "p50", "p75", "p85 (top-15%)", "p95 (top-5%)"]
    ):
        out.append(f"| {label} | {fmt(thr, 3)} | {fmt(er, 3)} | {fmt(p, 3)} | "
                   f"{fmt(rec, 3)} | {fmt(f1, 3)} | {nf} |")
    out.append("")

    # Candidate rules.
    rules = candidate_rules(pairs)
    out.append("### Candidate cutoff rules")
    out.append("")
    out.append("| rule | threshold | elevated_rate | precision | recall | F1 | n_flagged |")
    out.append("|---|---|---|---|---|---|---|")
    for key in ("rule_a", "rule_b", "rule_c"):
        name, thr, (_, er, p, rec, f1, nf) = rules[key]
        out.append(f"| {name} | {fmt(thr, 3)} | {fmt(er, 3)} | {fmt(p, 3)} | "
                   f"{fmt(rec, 3)} | {fmt(f1, 3)} | {nf} |")
    out.append("")

    # Recommendation. Heuristic: pick the rule with elevated_rate
    # in [0.10, 0.25] and the highest precision. If none qualifies,
    # fall back to top-15% (rule A) and flag for operator review.
    out.append("### Recommendation")
    out.append("")
    candidates = []
    for key in ("rule_a", "rule_b", "rule_c"):
        _, thr, (_, er, p, rec, f1, nf) = rules[key]
        if er is None or er != er:  # NaN guard
            continue
        if 0.10 <= er <= 0.25:
            candidates.append((p if p == p else 0, key, thr))
    if candidates:
        candidates.sort(reverse=True)
        _, key, thr = candidates[0]
        name = rules[key][0]
        out.append(f"**Recommended cutoff: `{name}` at `{fmt(thr, 3)}`** — "
                   f"falls inside the operational 10–25% elevated band and has the "
                   f"highest precision among the three candidates.")
    else:
        out.append(f"**No clear winner.** No candidate landed inside the "
                   f"operational 10–25% elevated band. Operator should review the "
                   f"sweep table and pick manually — possibly fall back to "
                   f"`unknown` until distribution matures, or set a per-tenant "
                   f"override.")
    out.append("")
    return "\n".join(out)


def main():
    out = []
    out.append("# Readiness Redesign — Phase 2 Chip Threshold Calibration")
    out.append("")
    out.append(f"Auto-generated by `analysis/phase8_chip_threshold_calibration/calibration.py`. "
               f"Schema: `{SCHEMA}`. Slice: `source_2025_current` "
               f"({TEST_START} → {TEST_END}).")
    out.append("")
    out.append("## Goal")
    out.append("")
    out.append("Pick the cutoff that maps each binary chip's `predicted_value` "
               "(positive event rate from `event_base_rate` baseline) to a "
               "user-visible state. Once chosen, the threshold lives in tenant "
               "settings (same shape as `chronic_load.min_acute_density` in "
               "§6.2) and the UI chip just consumes `value + threshold → state`.")
    out.append("")
    out.append("## Methodology")
    out.append("")
    out.append("- Join `naive_baselines.predicted_value` with the sibling "
               "`target_snapshots.target_value` (forward-window observed label) "
               "where both are populated and the target row is eligible.")
    out.append("- Sweep thresholds at distribution quartiles (p25 / p50 / p75 / "
               "p85 / p95) and at the three candidate cutoff rules:")
    out.append(f"  - **Rule A (top-15%)**: `predicted_value >= p85`")
    out.append(f"  - **Rule B (base_rate × 1.5)**: `predicted_value >= 1.5 × base_rate`")
    out.append(f"  - **Rule C (max-F1)**: threshold that maximises F1 on the slice")
    out.append("- Per target: pick the candidate rule whose elevated rate "
               "falls inside the operational 10–25% band and has the highest "
               "precision. The 10–25% band is the same one used to calibrate "
               "`chronic_load.min_acute_density` in PR #97 — keeps the chip "
               "useful (non-trivial fraction of days) without spamming.")
    out.append(f"- **Insufficient-data rule**: fewer than {MIN_PAIRS} eligible "
               f"pairs → chip stays `unknown` regardless of threshold. "
               f"Documented per target below.")
    out.append("")

    for sub, kind in TARGETS:
        try:
            pairs = load_pairs(sub, kind)
        except subprocess.CalledProcessError as e:
            out.append(f"## `{sub} / {kind}`")
            out.append("")
            out.append(f"**psql error**: {e.stderr.strip()}")
            out.append("")
            continue
        out.append(render_target(sub, kind, pairs))
    print("\n".join(out))


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as e:
        print(f"# Phase 2 chip threshold calibration — psql error\n\n{e.stderr}",
              file=sys.stderr)
        sys.exit(1)
