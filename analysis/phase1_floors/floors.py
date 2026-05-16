#!/usr/bin/env python3
"""Phase 1 baseline floors for the readiness redesign.

Reads Phase 0 target_snapshots and naive_baselines and answers one
question per target: how well does the best naive baseline do on a
time-based hold-out, and is there headroom for a trained model?

No production code is touched. One offline script + one markdown
report. Re-run any time to refresh the report against current data.

## Methodology

- Train range:  <= 2024-12-31 (used implicitly — the writers built
                naive baselines from this history; not separately
                evaluated here, since baselines ARE the train output).
- Test range:   2025-01-01 .. 2026-05-15.

Two evaluation slices:
  - test_2025_current:    rows with source_epoch = 'source_2025_current'.
                          The primary decision basis (2024 gap is a
                          known source anomaly, excluded from training
                          implications).
  - test_all_post_train:  every test-range row regardless of epoch.
                          Reported for context.

Continuous targets (Recovery rolling_3d, Passive rolling_3d):
  - MAE and RMSE per naive baseline_kind.
  - Bootstrap 95% CI on MAE (1000 resamples).

Classification targets (Acute event_t1_t3, event_strict_t1_t3,
Chronic chronic_label, chronic_acute_density):
  - Base rate on test slice.
  - Precision at recall = 0.5 (primary metric).
  - Lift over base rate at the same recall (secondary).
  - Event count and bootstrap CI for the precision (1000 resamples).
  - Persistence baseline (yesterday's label predicts today) computed
    inline; only event_base_rate is in the naive_baselines table.
  - AUC reported but not used as the decision criterion.

## Running

Required: psql on PATH and the usual libpq env vars (PGHOST, PGUSER,
PGDATABASE, PGPASSWORD, PGOPTIONS=--search_path=health). Same as the
operator runbook.

    python analysis/phase1_floors/floors.py \\
        > READINESS_REDESIGN_PHASE1_FLOORS.md

The script writes the report to stdout; redirect to overwrite the
versioned markdown.

Pure stdlib. No psycopg, no numpy.
"""

from __future__ import annotations

import math
import os
import random
import statistics
import subprocess
import sys
from collections import defaultdict
from dataclasses import dataclass
from datetime import date, datetime, timedelta
from typing import Iterable

# -------- config -------------------------------------------------------

PSQL = os.environ.get("PSQL", "psql")
TRAIN_END = date(2024, 12, 31)
TEST_START = date(2025, 1, 1)
TEST_END = date(2026, 5, 15)
BOOTSTRAP_ITERATIONS = 1000
BOOTSTRAP_SEED = 20260516
TARGET_RECALL = 0.5

# Sub-score / target_kind pairs the script analyses.
CONTINUOUS_TARGETS = [
    ("recovery_stability", "rolling_3d"),
    ("passive_efficiency", "rolling_3d"),
]
CLASSIFICATION_TARGETS = [
    ("acute_risk", "event_t1_t3"),
    ("acute_risk", "event_strict_t1_t3"),
    ("chronic_load", "chronic_label"),
    ("chronic_load", "chronic_acute_density"),
]

# -------- data access --------------------------------------------------

def psql(sql: str) -> list[list[str]]:
    """Run a query through psql and return rows as lists of strings.

    -A: unaligned, -F sep, -t no headers/footers, -X skip psqlrc.
    NULL is the literal empty string in this mode; the per-row parser
    below converts that to None.
    """
    result = subprocess.run(
        [PSQL, "-X", "-A", "-F", "\t", "-t", "-c", sql],
        check=True, capture_output=True, text=True,
    )
    out = []
    for line in result.stdout.splitlines():
        if not line:
            continue
        cells = [c if c != "" else None for c in line.split("\t")]
        out.append(cells)
    return out


@dataclass
class TargetRow:
    date: date
    value: float | None      # NULL when target is ineligible
    eligible: bool
    source_epoch: str


@dataclass
class BaselineRow:
    date: date
    baseline_kind: str
    predicted: float | None


def load_targets(sub_score: str, target_kind: str) -> list[TargetRow]:
    sql = f"""
        SELECT date, target_value, eligible, source_epoch
          FROM target_snapshots
         WHERE sub_score = '{sub_score}'
           AND target_kind = '{target_kind}'
           AND date BETWEEN '{TEST_START}' AND '{TEST_END}'
         ORDER BY date ASC
    """
    rows = []
    for r in psql(sql):
        d = datetime.strptime(r[0], "%Y-%m-%d").date()
        v = float(r[1]) if r[1] is not None else None
        eligible = (r[2] == "t")
        rows.append(TargetRow(d, v, eligible, r[3]))
    return rows


def load_baselines(sub_score: str, target_kind: str) -> dict[str, dict[date, float | None]]:
    """Returns baseline_kind -> {date -> predicted_value}."""
    sql = f"""
        SELECT date, baseline_kind, predicted_value
          FROM naive_baselines
         WHERE sub_score = '{sub_score}'
           AND target_kind = '{target_kind}'
           AND date BETWEEN '{TEST_START}' AND '{TEST_END}'
         ORDER BY date ASC
    """
    out: dict[str, dict[date, float | None]] = defaultdict(dict)
    for r in psql(sql):
        d = datetime.strptime(r[0], "%Y-%m-%d").date()
        bk = r[1]
        pv = float(r[2]) if r[2] is not None else None
        out[bk][d] = pv
    return out


# -------- stats helpers ------------------------------------------------

def mae(residuals: list[float]) -> float:
    if not residuals:
        return math.nan
    return sum(abs(r) for r in residuals) / len(residuals)


def rmse(residuals: list[float]) -> float:
    if not residuals:
        return math.nan
    return math.sqrt(sum(r * r for r in residuals) / len(residuals))


def bootstrap_ci(values: list[float], statistic, iterations: int = BOOTSTRAP_ITERATIONS) -> tuple[float, float]:
    if len(values) < 2:
        return (math.nan, math.nan)
    rng = random.Random(BOOTSTRAP_SEED)
    results = []
    n = len(values)
    for _ in range(iterations):
        sample = [values[rng.randint(0, n - 1)] for _ in range(n)]
        results.append(statistic(sample))
    results.sort()
    lo = results[int(0.025 * iterations)]
    hi = results[int(0.975 * iterations)]
    return (lo, hi)


# -------- continuous evaluation ---------------------------------------

def evaluate_continuous(sub_score: str, target_kind: str, slice_filter) -> dict:
    targets = [t for t in load_targets(sub_score, target_kind) if slice_filter(t)]
    eligible = [t for t in targets if t.eligible and t.value is not None]
    baselines = load_baselines(sub_score, target_kind)

    rows = []
    for bk in ("persistence_yesterday", "rolling_7d_mean", "rolling_30d_mean", "ewma_45d"):
        pred_map = baselines.get(bk, {})
        paired = [(t.value, pred_map.get(t.date))
                  for t in eligible
                  if pred_map.get(t.date) is not None]
        if not paired:
            rows.append({"baseline_kind": bk, "n": 0, "mae": math.nan,
                         "rmse": math.nan, "mae_ci": (math.nan, math.nan)})
            continue
        residuals = [a - p for (a, p) in paired]
        abs_residuals = [abs(r) for r in residuals]
        rows.append({
            "baseline_kind": bk,
            "n": len(paired),
            "mae": mae(residuals),
            "rmse": rmse(residuals),
            "mae_ci": bootstrap_ci(abs_residuals, lambda s: sum(s) / len(s)),
        })
    return {"sub_score": sub_score, "target_kind": target_kind,
            "eligible_count": len(eligible), "rows": rows}


# -------- classification evaluation -----------------------------------

def precision_at_recall(
    predicted_actual: list[tuple[float, int]],
    target_recall: float,
) -> tuple[float | None, int]:
    """Returns (precision, captured_positives) at the smallest threshold
    that achieves at least `target_recall`. Predictions higher = more
    likely positive.

    Tied scores are evaluated as a whole bucket — when several rows
    share the cutoff prediction, we include all of them or none of
    them. Counting an arbitrary prefix of the tie makes the result
    depend on input order and inflates precision on coarse baselines
    (`persistence_yesterday` produces only 0/1; `event_base_rate`
    has repeats).

    Returns (None, 0) when there are no positives.
    """
    total_positives = sum(a for _, a in predicted_actual)
    if total_positives == 0:
        return (None, 0)
    sorted_pa = sorted(predicted_actual, key=lambda x: -x[0])
    target_count = target_recall * total_positives
    cum_p, cum_n = 0, 0
    i = 0
    while i < len(sorted_pa):
        # Tie group: all rows sharing this prediction value.
        j = i
        while j + 1 < len(sorted_pa) and sorted_pa[j + 1][0] == sorted_pa[i][0]:
            j += 1
        bucket_pos = sum(a for _, a in sorted_pa[i : j + 1])
        bucket_n = j - i + 1
        cum_p += bucket_pos
        cum_n += bucket_n
        if cum_p >= target_count:
            return (cum_p / cum_n, cum_p)
        i = j + 1
    return (None, cum_p)


def auc(predicted_actual: list[tuple[float, int]]) -> float | None:
    """Trapezoidal AUC. Returns None when one class is missing."""
    positives = [p for p, a in predicted_actual if a == 1]
    negatives = [p for p, a in predicted_actual if a == 0]
    if not positives or not negatives:
        return None
    # Mann-Whitney U statistic / (n_pos * n_neg)
    combined = sorted([(p, 1) for p in positives] + [(p, 0) for p in negatives])
    # Average rank for ties
    ranks: list[float] = [0.0] * len(combined)
    i = 0
    while i < len(combined):
        j = i
        while j + 1 < len(combined) and combined[j + 1][0] == combined[i][0]:
            j += 1
        avg_rank = (i + j) / 2 + 1
        for k in range(i, j + 1):
            ranks[k] = avg_rank
        i = j + 1
    pos_rank_sum = sum(r for r, (_, a) in zip(ranks, combined) if a == 1)
    n_pos = len(positives)
    n_neg = len(negatives)
    u = pos_rank_sum - n_pos * (n_pos + 1) / 2
    return u / (n_pos * n_neg)


def evaluate_classification(sub_score: str, target_kind: str, slice_filter) -> dict:
    targets = [t for t in load_targets(sub_score, target_kind) if slice_filter(t)]
    eligible = [t for t in targets if t.eligible and t.value is not None]
    baselines = load_baselines(sub_score, target_kind)

    total = len(eligible)
    positives = sum(1 for t in eligible if t.value >= 0.5)
    base_rate = positives / total if total else math.nan

    # Persistence baseline (inline): yesterday's label predicts today.
    eligible_sorted = sorted(eligible, key=lambda t: t.date)
    by_date = {t.date: int(t.value >= 0.5) for t in eligible_sorted}
    persistence_pa: list[tuple[float, int]] = []
    for t in eligible_sorted:
        prev = t.date - timedelta(days=1)
        if prev in by_date:
            persistence_pa.append((float(by_date[prev]), int(t.value >= 0.5)))

    def metrics(pa: list[tuple[float, int]]) -> dict:
        if not pa:
            return {"n": 0, "precision": None, "lift": None,
                    "auc": None, "precision_ci": (math.nan, math.nan),
                    "captured": 0}
        prec, captured = precision_at_recall(pa, TARGET_RECALL)
        lift = (prec / base_rate) if (prec is not None and base_rate > 0) else None

        # Stratified bootstrap for precision@recall: resample positives
        # and negatives separately so class counts stay constant across
        # iterations. Pooled resampling on sparse targets (event_strict
        # has only 9 positives) would let the bootstrap drift the
        # positive rate itself, mixing prevalence noise with ranking
        # uncertainty. Plan §3.5 calls for stratified bootstrap on
        # rare events explicitly.
        positives = [p for p in pa if p[1] == 1]
        negatives = [p for p in pa if p[1] == 0]
        if not positives or not negatives:
            return {"n": len(pa), "precision": prec, "lift": lift,
                    "auc": auc(pa),
                    "precision_ci": (math.nan, math.nan),
                    "captured": captured}
        rng = random.Random(BOOTSTRAP_SEED)
        n_pos = len(positives)
        n_neg = len(negatives)
        results = []
        for _ in range(BOOTSTRAP_ITERATIONS):
            sample = [positives[rng.randint(0, n_pos - 1)] for _ in range(n_pos)]
            sample += [negatives[rng.randint(0, n_neg - 1)] for _ in range(n_neg)]
            p_b, _ = precision_at_recall(sample, TARGET_RECALL)
            if p_b is not None:
                results.append(p_b)
        if results:
            results.sort()
            lo = results[int(0.025 * len(results))]
            hi = results[int(0.975 * len(results))]
        else:
            lo, hi = math.nan, math.nan
        return {"n": len(pa), "precision": prec, "lift": lift,
                "auc": auc(pa), "precision_ci": (lo, hi), "captured": captured}

    rows = []
    for bk in ("event_base_rate",):
        pred_map = baselines.get(bk, {})
        pa = [(pred_map[t.date], int(t.value >= 0.5))
              for t in eligible
              if pred_map.get(t.date) is not None]
        m = metrics(pa)
        m["baseline_kind"] = bk
        rows.append(m)
    pm = metrics(persistence_pa)
    pm["baseline_kind"] = "persistence_yesterday (inline)"
    rows.append(pm)

    return {
        "sub_score": sub_score, "target_kind": target_kind,
        "total": total, "positives": positives, "base_rate": base_rate,
        "rows": rows,
    }


# -------- rendering ----------------------------------------------------

def fmt(x, places=4):
    if x is None or (isinstance(x, float) and math.isnan(x)):
        return "—"
    return f"{x:.{places}f}"


def render_continuous(title: str, result: dict) -> str:
    out = [f"### {title}", ""]
    out.append(f"Eligible test rows: **{result['eligible_count']}**")
    out.append("")
    out.append("| baseline_kind | n | MAE | RMSE | MAE 95% bootstrap CI |")
    out.append("|---|---|---|---|---|")
    for r in result["rows"]:
        lo, hi = r["mae_ci"]
        ci = f"[{fmt(lo)}, {fmt(hi)}]" if not math.isnan(lo) else "—"
        out.append(f"| {r['baseline_kind']} | {r['n']} | {fmt(r['mae'])} | {fmt(r['rmse'])} | {ci} |")
    # Best baseline summary.
    valid = [r for r in result["rows"] if not math.isnan(r["mae"]) and r["n"] > 0]
    if valid:
        best = min(valid, key=lambda r: r["mae"])
        out.append("")
        out.append(f"Best naive baseline (lowest MAE): **`{best['baseline_kind']}`** at MAE = {fmt(best['mae'])}.")
    out.append("")
    return "\n".join(out)


def render_classification(title: str, result: dict) -> str:
    out = [f"### {title}", ""]
    out.append(f"Eligible test rows: **{result['total']}**, positives: **{result['positives']}**, base rate: **{fmt(result['base_rate'], 4)}**")
    out.append("")
    out.append(f"Metric: precision at recall = {TARGET_RECALL}, with lift over base rate at the same recall.")
    out.append("")
    out.append("| baseline_kind | n paired | precision@R=0.5 | lift | AUC | precision 95% CI | captured pos |")
    out.append("|---|---|---|---|---|---|---|")
    for r in result["rows"]:
        lo, hi = r["precision_ci"]
        ci = f"[{fmt(lo, 3)}, {fmt(hi, 3)}]" if not math.isnan(lo) else "—"
        out.append(
            f"| {r['baseline_kind']} | {r['n']} | "
            f"{fmt(r['precision'], 3)} | {fmt(r['lift'], 2)} | "
            f"{fmt(r['auc'], 3)} | {ci} | {r['captured']} |"
        )
    valid = [r for r in result["rows"] if r["precision"] is not None]
    if valid:
        best = max(valid, key=lambda r: r["precision"])
        out.append("")
        out.append(f"Best naive baseline (highest precision@recall=0.5): **`{best['baseline_kind']}`** "
                   f"at precision = {fmt(best['precision'], 3)} (lift {fmt(best['lift'], 2)}× over base rate).")
    out.append("")
    return "\n".join(out)


# -------- main ---------------------------------------------------------

def classify_continuous_headroom(best_row: dict, target_sd: float | None) -> str:
    """Derive a one-word headroom verdict for a continuous target.

    Driven by the ratio of best-baseline MAE to target SD on the test
    slice. The thresholds (40% / 70%) match the report's prior verdicts
    and are stable across reasonable changes to the data — the script
    will re-classify automatically when the underlying numbers move.
    """
    mae = best_row.get("mae")
    if mae is None or (isinstance(mae, float) and math.isnan(mae)):
        return "insufficient evidence"
    if target_sd is None or target_sd <= 0:
        return "unknown"
    ratio = mae / target_sd
    if ratio < 0.4:
        return "potentially significant"
    if ratio < 0.7:
        return "modest"
    return "low (close to noise floor)"


def classify_classifier_headroom(base_row: dict, positives: int) -> str:
    """Derive a one-word headroom verdict for a classifier target from
    the `event_base_rate` baseline. AUC + n positives are the inputs.
    Persistence is intentionally NOT used (forward-window overlap is
    not real predictive signal, see the interpretation note in the
    report)."""
    if positives < 20:
        return "insufficient evidence"
    a = base_row.get("auc")
    if a is None:
        return "unknown"
    if a < 0.5:
        return "none (label likely mis-tuned)"
    if a < 0.55:
        return "very low"
    if a < 0.60:
        return "low"
    if a < 0.70:
        return "modest"
    return "potentially significant"


def main():
    def in_test_window(t: TargetRow) -> bool:
        return TEST_START <= t.date <= TEST_END

    def slice_all_post_train(t: TargetRow) -> bool:
        return in_test_window(t)

    def slice_2025_current(t: TargetRow) -> bool:
        return in_test_window(t) and t.source_epoch == "source_2025_current"

    # ---- 1. Collect all results into structured form. ---------------
    # Renderers and the decision summary BOTH read from this so the
    # report cannot drift between sections on a re-run.
    slices = [
        ("test_2025_current", slice_2025_current),
        ("test_all_post_train", slice_all_post_train),
    ]
    continuous_results: dict[tuple[str, str, str], dict] = {}
    classifier_results: dict[tuple[str, str, str], dict] = {}
    # Target SDs are needed to characterise continuous headroom. They
    # come from the eligible test rows themselves so the verdict moves
    # with the data.
    target_sds: dict[tuple[str, str, str], float] = {}

    for slice_name, slice_fn in slices:
        for sub, tk in CONTINUOUS_TARGETS:
            res = evaluate_continuous(sub, tk, slice_fn)
            continuous_results[(sub, tk, slice_name)] = res
            # Compute target SD from the eligible test rows.
            targets = [t for t in load_targets(sub, tk)
                       if slice_fn(t) and t.eligible and t.value is not None]
            if len(targets) >= 2:
                target_sds[(sub, tk, slice_name)] = statistics.pstdev(t.value for t in targets)
        for sub, tk in CLASSIFICATION_TARGETS:
            res = evaluate_classification(sub, tk, slice_fn)
            classifier_results[(sub, tk, slice_name)] = res

    # ---- 2. Render report. ------------------------------------------
    md = []
    md.append("# Readiness Redesign — Phase 1 Baseline Floors")
    md.append("")
    md.append("Auto-generated by `analysis/phase1_floors/floors.py`. Re-run any time to refresh against current `target_snapshots` + `naive_baselines`.")
    md.append("")
    md.append("## Methodology")
    md.append("")
    md.append(f"- Train window (used to BUILD naive baselines by the writers): up to {TRAIN_END}.")
    md.append(f"- Test window: {TEST_START} to {TEST_END}.")
    md.append("- Two test slices:")
    md.append("  - **`test_2025_current`** — rows with `source_epoch = source_2025_current` only. Primary decision basis. 2024 gap (`source_2024_gap`) is a known source anomaly per the source_epochs catalogue.")
    md.append("  - **`test_all_post_train`** — every row in the test window regardless of epoch. Reported for context.")
    md.append(f"- Continuous: MAE / RMSE per `baseline_kind`; bootstrap CI on MAE with {BOOTSTRAP_ITERATIONS} resamples.")
    md.append(f"- Classification: precision at recall = {TARGET_RECALL} (primary), lift over base rate (secondary), AUC (supplementary, not a decision criterion). **Stratified bootstrap** (positives and negatives resampled separately) preserves class counts on sparse targets like `event_strict_t1_t3`.")
    md.append("")

    for slice_name, _ in slices:
        md.append(f"## Slice: `{slice_name}`")
        md.append("")
        md.append("### Continuous targets — MAE/RMSE floors")
        md.append("")
        for sub, tk in CONTINUOUS_TARGETS:
            md.append(render_continuous(f"{sub} / {tk}", continuous_results[(sub, tk, slice_name)]))
        md.append("### Classification targets — precision@recall=0.5 floors")
        md.append("")
        for sub, tk in CLASSIFICATION_TARGETS:
            md.append(render_classification(f"{sub} / {tk}", classifier_results[(sub, tk, slice_name)]))

    md.append("## Interpretation note: `persistence_yesterday` for classification is a window-overlap artifact, not a real floor")
    md.append("")
    md.append("`persistence_yesterday` scores extremely well on every classification target. That does NOT reflect predictive signal. The forward-window labels overlap heavily between adjacent dates:")
    md.append("")
    md.append("- `event_t1_t3` for date `t` covers `t+1..t+3`; for date `t+1` covers `t+2..t+4`. **2 of 3 days shared**, so adjacent labels are by construction correlated. Persistence is exploiting label-window overlap, not predicting unseen physiology.")
    md.append("- `event_strict_t1_t3` same overlap shape.")
    md.append("- `chronic_label` and `chronic_acute_density` look 14 days forward; adjacent labels share 13 of 14 days. Persistence is essentially predicting the overlap, which trivially carries.")
    md.append("")
    md.append("**The honest classification floor is `event_base_rate`** (the 90-day rolling prior probability). Its lift over base rate at recall = 0.5 is the real benchmark a model must beat to add value.")
    md.append("")

    # ---- 3. Decision summary — DERIVED from collected results. ------
    # This table updates automatically when target_snapshots /
    # naive_baselines change and the script is re-run. Verdict labels
    # come from classify_*_headroom helpers above.
    md.append("## Decision summary")
    md.append("")
    md.append("Floors a future model must beat on the **`test_2025_current`** slice. For classification, the floor is `event_base_rate` (per the note above); persistence is reported in the tables for transparency but is not the decision metric.")
    md.append("")
    md.append("| target | type | floor (best naive) | floor metric | floor CI half-width | model headroom? |")
    md.append("|---|---|---|---|---|---|")

    primary_slice = "test_2025_current"

    def fmt_ci_halfwidth(lo: float, hi: float) -> str:
        if math.isnan(lo) or math.isnan(hi):
            return "—"
        return f"±{(hi - lo) / 2:.3f}"

    for sub, tk in CONTINUOUS_TARGETS:
        res = continuous_results[(sub, tk, primary_slice)]
        valid = [r for r in res["rows"] if r["n"] > 0 and not math.isnan(r["mae"])]
        if not valid:
            md.append(f"| {sub} / {tk} | continuous | — | — | — | insufficient evidence |")
            continue
        best = min(valid, key=lambda r: r["mae"])
        lo, hi = best["mae_ci"]
        sd = target_sds.get((sub, tk, primary_slice))
        sd_part = f", target SD {sd:.3f}" if sd is not None else ""
        verdict = classify_continuous_headroom(best, sd)
        md.append(
            f"| {sub} / {tk} | continuous | `{best['baseline_kind']}` "
            f"| MAE {best['mae']:.4f} | {fmt_ci_halfwidth(lo, hi)} "
            f"| **{verdict}** — model needs MAE materially below CI lower bound{sd_part} |"
        )

    for sub, tk in CLASSIFICATION_TARGETS:
        res = classifier_results[(sub, tk, primary_slice)]
        base_row = next((r for r in res["rows"] if r["baseline_kind"] == "event_base_rate"), None)
        if base_row is None or base_row["precision"] is None:
            md.append(f"| {sub} / {tk} | binary | — | — | — | insufficient evidence |")
            continue
        lo, hi = base_row["precision_ci"]
        verdict = classify_classifier_headroom(base_row, res["positives"])
        lift_str = f"{base_row['lift']:.2f}×" if base_row["lift"] is not None else "—"
        auc_str = f"AUC {base_row['auc']:.3f}" if base_row["auc"] is not None else "AUC —"
        positives_note = f"; {res['positives']} positives in {res['total']} rows"
        md.append(
            f"| {sub} / {tk} | binary | `event_base_rate` "
            f"| precision {base_row['precision']:.3f}, lift {lift_str} "
            f"| {fmt_ci_halfwidth(lo, hi)} "
            f"| **{verdict}** — {auc_str}{positives_note} |"
        )

    md.append("")
    md.append("Headroom verdicts derived programmatically:")
    md.append("- Continuous: by MAE / target SD ratio (`<0.4` potentially significant, `<0.7` modest, otherwise low).")
    md.append("- Classifier: by `event_base_rate` AUC + positive count (`positives<20` insufficient evidence; AUC `<0.5` label likely mis-tuned; `<0.55` very low; `<0.60` low; `<0.70` modest; `≥0.70` potentially significant).")
    md.append("")

    md.append("## Open follow-ups (carried + new)")
    md.append("")
    md.append("- **Retune `chronic_acute_density` threshold** if its positive rate is far from the 15–30% operationally-useful band. Raise event count threshold; bump `chronicLoadFormulaVersion`; re-backfill via the admin endpoint; re-run floors. The decision summary above will reflect the change automatically.")
    md.append("- **`event_strict_t1_t3` is too sparse for an independent classifier** while positives stay below ~30. Either accept that strict remains a silent diagnostic only, or relax the strict criterion (e.g. ±1.0σ instead of ±1.5σ) so it becomes informative for Phase 1.")
    md.append("- **Investigate 2022 strict event spike** (5.7% vs 1–2% in other years). Possible illness cluster / lifestyle change / sensor artifact. Narrative review before feeding into trained models.")
    md.append("- **Continuous floors are close to noise floor** if MAE / target SD exceeds 0.7. Phase 1 should ask whether the extra features (sleep architecture, intraday HR variability) actually shift this number.")
    md.append("- **Persistence is not a useful baseline for forward-window classification labels** — the window-overlap math dominates. Either drop the persistence column from the report or change classifier labels to disjoint windows (`t+1` instead of `t+1..t+3`) if persistence-style autocorrelation is needed as a real floor.")

    print("\n".join(md))


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as e:
        print(f"# Phase 1 Floors — psql error\n\n{e.stderr}", file=sys.stderr)
        sys.exit(1)
