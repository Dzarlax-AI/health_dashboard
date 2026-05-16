#!/usr/bin/env python3
"""Phase 1 step 3 — GBM signal-probe on `chronic_label`.

**Scope contract.** This script is a one-shot signal-probe for non-linear
signal on the single best linear-AUC target. Not a "let's try ML" run.

Rules baked in (do NOT relax mid-run):

- Target: `chronic_load / chronic_label` only.
- Features: own-features from the Phase 0 Chronic Load writer (same
  10 keys as `phase5_chronic_label_feasibility/feasibility.py`). No
  cross-sub_score features.
- Model: `sklearn.ensemble.GradientBoostingClassifier` with a frozen
  16-cell grid:
      max_depth        in {2, 3}
      learning_rate    in {0.03, 0.1}
      n_estimators     in {50, 100}
      min_samples_leaf in {5, 10}
- Hyperparameters chosen ONCE via inner train/val (chronological 80/20
  inside the train period). Selected cell is refit on the full train
  period and scored ONCE on the held-out test set.
- Primary criterion (same as the linear feasibility scripts):
      model precision@R=0.5 LOWER CI > floor precision@R=0.5 UPPER CI.
  Single-point lift below CI overlap is statistical noise.
- Secondary metrics reported for interpretation, not decision: AUC,
  precision@R=0.25, top-k precision for k in {5, 10, 20}.
- Floor: `event_base_rate` on the same test rows (model and floor
  scored on identical rows via eval_mask, same convention as phase4-6).
- Bootstrap: stratified (positives and negatives resampled separately),
  1000 iterations.
- Walk-forward sanity check: expanding monthly blocks. Hyperparameters
  selected per month via inner train/val on the cumulative-train
  window (no test-set selection). Floor reported alongside model on
  the same month rows.

**Stop rule.** If primary criterion does not clear, this is the final
Phase 1 increment. We do not run GBM on chronic_acute_density or
acute_risk — those targets had even weaker linear AUC lift and the
hypothesis "non-linear signal exists" is least supported there.

Deps: numpy + scikit-learn (sklearn introduced here for the probe; the
linear scripts in phase2-6 stayed pure-numpy by design and that
convention is unchanged for them). Version pins live next to this
file in `requirements.txt` — install them before the first run, the
default analysis Python on this machine does not carry sklearn.

## Running

    # one-time setup in this analysis subdir's venv / interpreter:
    python -m pip install -r analysis/phase7_chronic_label_gbm_probe/requirements.txt

    PSQL=/path/to/psql.exe PYTHONUTF8=1 python \\
        analysis/phase7_chronic_label_gbm_probe/probe.py \\
        > READINESS_REDESIGN_PHASE1_CHRONIC_LABEL_GBM_PROBE.md

Uses libpq env vars (PGHOST/PGUSER/PGDATABASE/PGPASSWORD/PGOPTIONS).
"""

from __future__ import annotations

import itertools
import json
import math
import os
import random
import subprocess
import sys
from collections import defaultdict
from dataclasses import dataclass
from datetime import date, datetime

import numpy as np
from sklearn.ensemble import GradientBoostingClassifier

# -------- config -------------------------------------------------------

PSQL = os.environ.get("PSQL", "psql")
TEST_START = date(2025, 1, 1)
TEST_END = date(2026, 5, 15)
PRIMARY_SPLIT_RATIO = 0.70
BOOTSTRAP_ITERATIONS = 1000
BOOTSTRAP_SEED = 20260516
TARGET_RECALL = 0.5
SECONDARY_RECALL = 0.25
TOP_K_LIST = [5, 10, 20]

# 16-cell frozen grid. Listed explicitly so the contract is visible at the
# top of the file — not via Cartesian product over abstract lists.
GBM_GRID: list[dict] = [
    {"max_depth": md, "learning_rate": lr,
     "n_estimators": ne, "min_samples_leaf": ml}
    for md in (2, 3)
    for lr in (0.03, 0.1)
    for ne in (50, 100)
    for ml in (5, 10)
]
GBM_RANDOM_STATE = 20260516

SUB_SCORE = "chronic_load"
TARGET_KIND = "chronic_label"

# Feature keys — same set used by phase5 linear feasibility, no cross-
# sub_score features per the probe contract.
FEATURE_KEYS = [
    "recovery_3d_today",
    "recovery_3d_baseline_mean_45d",
    "recovery_3d_baseline_sd_45d",
    "recovery_3d_z_today",
    "recovery_eligible_count_45d",
    "recovery_eligible_count_180d",
    "paired_count_to_t",
    "warmup_met",
    "warmup_complete_45d",
    "warmup_complete_180d",
]

# -------- data access --------------------------------------------------


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


@dataclass
class Row:
    date: date
    label: int
    features: dict[str, float | None]


def load_rows() -> list[Row]:
    sql = f"""
        SELECT t.date,
               t.target_value,
               f.features::text
          FROM target_snapshots t
          JOIN feature_snapshots f
            ON f.date = t.date
           AND f.sub_score = t.sub_score
           AND f.source_epoch = t.source_epoch
         WHERE t.sub_score = '{SUB_SCORE}'
           AND t.target_kind = '{TARGET_KIND}'
           AND t.eligible = TRUE
           AND t.target_value IS NOT NULL
           AND t.source_epoch = 'source_2025_current'
           AND t.date BETWEEN '{TEST_START}' AND '{TEST_END}'
         ORDER BY t.date ASC
    """
    rows: list[Row] = []
    for r in psql(sql):
        d = datetime.strptime(r[0], "%Y-%m-%d").date()
        label = 1 if float(r[1]) >= 0.5 else 0
        feats_raw = json.loads(r[2])
        feats: dict[str, float | None] = {}
        for key in FEATURE_KEYS:
            v = feats_raw.get(key)
            if v is None:
                feats[key] = None
            elif isinstance(v, bool):
                feats[key] = 1.0 if v else 0.0
            else:
                feats[key] = float(v)
        rows.append(Row(d, label, feats))
    return rows


def load_event_base_rate() -> dict[date, float]:
    sql = f"""
        SELECT date, predicted_value
          FROM naive_baselines
         WHERE sub_score = '{SUB_SCORE}'
           AND target_kind = '{TARGET_KIND}'
           AND baseline_kind = 'event_base_rate'
           AND source_epoch = 'source_2025_current'
           AND date BETWEEN '{TEST_START}' AND '{TEST_END}'
    """
    out: dict[date, float] = {}
    for r in psql(sql):
        d = datetime.strptime(r[0], "%Y-%m-%d").date()
        if r[1] is not None:
            out[d] = float(r[1])
    return out


# -------- feature matrix -----------------------------------------------


def to_matrix(rows: list[Row]) -> tuple[np.ndarray, np.ndarray, list[date]]:
    keep = [r for r in rows if all(r.features[k] is not None for k in FEATURE_KEYS)]
    if not keep:
        return np.zeros((0, len(FEATURE_KEYS))), np.zeros((0,), dtype=int), []
    X = np.array(
        [[float(r.features[k]) for k in FEATURE_KEYS] for r in keep],
        dtype=np.float64,
    )
    y = np.array([r.label for r in keep], dtype=np.int64)
    dates = [r.date for r in keep]
    return X, y, dates


# -------- metrics ------------------------------------------------------


def precision_at_recall(predicted_actual: list[tuple[float, int]],
                        target_recall: float) -> tuple[float | None, int]:
    """Smallest-threshold precision at the requested recall; tied
    predictions treated as a single bucket."""
    total_positives = sum(a for _, a in predicted_actual)
    if total_positives == 0:
        return (None, 0)
    sorted_pa = sorted(predicted_actual, key=lambda x: -x[0])
    target_count = target_recall * total_positives
    cum_p, cum_n = 0, 0
    i = 0
    while i < len(sorted_pa):
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


def auc_score(predicted_actual: list[tuple[float, int]]) -> float | None:
    positives = [p for p, a in predicted_actual if a == 1]
    negatives = [p for p, a in predicted_actual if a == 0]
    if not positives or not negatives:
        return None
    combined = sorted([(p, 1) for p in positives] + [(p, 0) for p in negatives])
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


def top_k_precision(predicted_actual: list[tuple[float, int]], k: int) -> float | None:
    if k <= 0 or k > len(predicted_actual):
        return None
    sorted_pa = sorted(predicted_actual, key=lambda x: -x[0])
    top = sorted_pa[:k]
    return sum(a for _, a in top) / k


def stratified_bootstrap_precision(
    predicted_actual: list[tuple[float, int]],
    target_recall: float,
    iterations: int = BOOTSTRAP_ITERATIONS,
    seed: int = BOOTSTRAP_SEED,
) -> tuple[float, float]:
    positives = [p for p in predicted_actual if p[1] == 1]
    negatives = [p for p in predicted_actual if p[1] == 0]
    if not positives or not negatives:
        return math.nan, math.nan
    rng = random.Random(seed)
    n_pos = len(positives)
    n_neg = len(negatives)
    results = []
    for _ in range(iterations):
        sample = [positives[rng.randint(0, n_pos - 1)] for _ in range(n_pos)]
        sample += [negatives[rng.randint(0, n_neg - 1)] for _ in range(n_neg)]
        p_b, _ = precision_at_recall(sample, target_recall)
        if p_b is not None:
            results.append(p_b)
    if not results:
        return math.nan, math.nan
    results.sort()
    lo = results[int(0.025 * len(results))]
    hi = results[int(0.975 * len(results))]
    return lo, hi


# -------- GBM helpers --------------------------------------------------


def fit_gbm(X: np.ndarray, y: np.ndarray, cell: dict) -> GradientBoostingClassifier:
    """Fit one grid cell. random_state pinned so the probe is
    reproducible bit-for-bit on the same DB snapshot."""
    model = GradientBoostingClassifier(
        max_depth=cell["max_depth"],
        learning_rate=cell["learning_rate"],
        n_estimators=cell["n_estimators"],
        min_samples_leaf=cell["min_samples_leaf"],
        random_state=GBM_RANDOM_STATE,
    )
    model.fit(X, y)
    return model


def proba(model: GradientBoostingClassifier, X: np.ndarray) -> np.ndarray:
    return model.predict_proba(X)[:, 1]


# -------- primary split -----------------------------------------------


def primary_split_and_evaluate(rows: list[Row], baseline_map: dict[date, float]) -> dict:
    X, y, dates = to_matrix(rows)
    n = len(dates)
    cut = int(round(PRIMARY_SPLIT_RATIO * n))
    if cut < 10 or n - cut < 5:
        return {"error": f"not enough rows for primary split: n={n}, cut={cut}"}

    train_dates = dates[:cut]
    test_dates = dates[cut:]
    X_train, y_train = X[:cut], y[:cut]
    X_test, y_test = X[cut:], y[cut:]

    inner_cut = int(round(0.8 * cut))
    if inner_cut < 5 or cut - inner_cut < 5:
        return {"error": f"not enough rows for inner split: cut={cut}, inner_cut={inner_cut}"}

    X_inner_train = X[:inner_cut]
    y_inner_train = y[:inner_cut]
    X_val = X[inner_cut:cut]
    y_val = y[inner_cut:cut]

    # GBM requires both classes in the training fold. With sparse-positive
    # targets and a chronological inner split, a single-class window is
    # not pathological — it can happen on early backfills before positives
    # accumulate. Fail with an explanatory error rather than letting
    # sklearn raise mid-grid.
    if len(set(y_inner_train.tolist())) < 2:
        return {"error": f"inner train fold has only one class (n={inner_cut}); cannot fit GBM grid"}
    if len(set(y_train.tolist())) < 2:
        return {"error": f"full train fold has only one class (n={cut}); cannot refit chosen cell"}

    cell_val: list[tuple[dict, float | None, float | None]] = []
    for cell in GBM_GRID:
        model = fit_gbm(X_inner_train, y_inner_train, cell)
        val_probs = proba(model, X_val)
        pa = list(zip(val_probs.tolist(), y_val.tolist()))
        prec, _ = precision_at_recall(pa, TARGET_RECALL)
        a = auc_score(pa)
        cell_val.append((cell, prec, a))

    has_valid = any(prec is not None for _, prec, _ in cell_val)
    if has_valid:
        valid = [(cell, prec, a) for cell, prec, a in cell_val if prec is not None]
        chosen_cell, _, _ = max(valid, key=lambda t: (t[1], t[2] if t[2] is not None else -1))
    else:
        with_auc = [(cell, prec, a) for cell, prec, a in cell_val if a is not None]
        if not with_auc:
            return {"error": "val fold is degenerate; no metric defined for any cell"}
        chosen_cell, _, _ = max(with_auc, key=lambda t: t[2])

    final = fit_gbm(X_train, y_train, chosen_cell)
    test_probs_all = proba(final, X_test)

    eval_mask = np.array([baseline_map.get(d) is not None for d in test_dates])
    if eval_mask.sum() == 0:
        return {"error": "no test rows with floor predictions; cannot compare"}
    dropped_for_floor = int((~eval_mask).sum())

    test_probs = test_probs_all[eval_mask]
    y_test_eval = y_test[eval_mask]
    test_dates_eval = [d for d, keep in zip(test_dates, eval_mask) if keep]

    model_pa = list(zip(test_probs.tolist(), y_test_eval.tolist()))
    model_prec, model_captured = precision_at_recall(model_pa, TARGET_RECALL)
    model_prec_secondary, _ = precision_at_recall(model_pa, SECONDARY_RECALL)
    model_auc = auc_score(model_pa)
    model_ci = stratified_bootstrap_precision(model_pa, TARGET_RECALL)
    model_topk = {k: top_k_precision(model_pa, k) for k in TOP_K_LIST}

    floor_pa = [(baseline_map[d], int(y_test_eval[i])) for i, d in enumerate(test_dates_eval)]
    floor_prec, floor_captured = precision_at_recall(floor_pa, TARGET_RECALL)
    floor_prec_secondary, _ = precision_at_recall(floor_pa, SECONDARY_RECALL)
    floor_auc = auc_score(floor_pa)
    floor_ci = stratified_bootstrap_precision(floor_pa, TARGET_RECALL)
    floor_topk = {k: top_k_precision(floor_pa, k) for k in TOP_K_LIST}

    test_positives = int(y_test_eval.sum())
    test_base_rate = test_positives / len(y_test_eval) if len(y_test_eval) else math.nan

    return {
        "n_total": n,
        "train_range": (train_dates[0], train_dates[-1]),
        "test_range": (test_dates_eval[0], test_dates_eval[-1]),
        "n_train": cut,
        "n_test": int(eval_mask.sum()),
        "n_test_dropped_for_floor": dropped_for_floor,
        "test_positives": test_positives,
        "test_base_rate": test_base_rate,
        "inner_train_size": inner_cut,
        "inner_val_size": cut - inner_cut,
        "cell_val": cell_val,
        "chosen_cell": chosen_cell,
        "model": {
            "precision": model_prec, "ci": model_ci, "auc": model_auc,
            "precision_secondary": model_prec_secondary,
            "topk": model_topk,
            "captured": model_captured,
            "lift": (model_prec / test_base_rate) if (model_prec is not None and test_base_rate > 0) else None,
        },
        "floor": {
            "precision": floor_prec, "ci": floor_ci, "auc": floor_auc,
            "precision_secondary": floor_prec_secondary,
            "topk": floor_topk,
            "captured": floor_captured,
            "lift": (floor_prec / test_base_rate) if (floor_prec is not None and test_base_rate > 0) else None,
        },
    }


# -------- walk-forward sanity check -----------------------------------


def walk_forward(rows: list[Row], baseline_map: dict[date, float]) -> dict:
    """Same shape as phase5: per-month inner train/val for cell
    selection, model and floor on identical month rows."""
    X, y, dates = to_matrix(rows)
    if not dates:
        return {"error": "no rows"}
    by_month: dict[str, list[int]] = defaultdict(list)
    for i, d in enumerate(dates):
        by_month[d.strftime("%Y-%m")].append(i)
    months = sorted(by_month.keys())
    if len(months) < 3:
        return {"error": f"only {len(months)} months; need >= 3"}

    results = []
    for k in range(1, len(months)):
        test_month = months[k]
        train_idx = []
        for m in months[:k]:
            train_idx.extend(by_month[m])
        test_idx = by_month[test_month]
        if len(train_idx) < 30 or len(test_idx) < 5:
            continue
        test_dates_month = [dates[i] for i in test_idx]
        keep = [j for j, d in enumerate(test_dates_month) if baseline_map.get(d) is not None]
        if len(keep) < 5:
            continue
        test_idx = [test_idx[j] for j in keep]
        test_dates_month = [test_dates_month[j] for j in keep]
        X_train, y_train = X[train_idx], y[train_idx]
        X_test, y_test = X[test_idx], y[test_idx]
        if y_test.sum() == 0 or y_test.sum() == len(y_test):
            continue

        inner_cut = int(round(0.8 * len(train_idx)))
        if inner_cut < 10 or len(train_idx) - inner_cut < 5:
            continue
        X_inner_train = X_train[:inner_cut]
        y_inner_train = y_train[:inner_cut]
        X_val = X_train[inner_cut:]
        y_val = y_train[inner_cut:]
        # Skip months where the chronological cumulative train or its
        # inner-train slice is single-class — GBM can't fit, and these
        # are inherently uninformative folds rather than errors.
        if len(set(y_inner_train.tolist())) < 2 or len(set(y_train.tolist())) < 2:
            continue

        cell_val_local = []
        for cell in GBM_GRID:
            mdl = fit_gbm(X_inner_train, y_inner_train, cell)
            val_probs = proba(mdl, X_val)
            pa = list(zip(val_probs.tolist(), y_val.tolist()))
            prec, _ = precision_at_recall(pa, TARGET_RECALL)
            cell_val_local.append((cell, prec))
        valid = [(c, p) for c, p in cell_val_local if p is not None]
        if valid:
            chosen_cell, _ = max(valid, key=lambda t: t[1])
        else:
            chosen_cell = GBM_GRID[0]  # arbitrary safe default

        final = fit_gbm(X_train, y_train, chosen_cell)
        test_probs = proba(final, X_test)
        model_pa = list(zip(test_probs.tolist(), y_test.tolist()))
        model_prec, _ = precision_at_recall(model_pa, TARGET_RECALL)

        floor_pa = [(baseline_map[d], int(y_test[i])) for i, d in enumerate(test_dates_month)]
        floor_prec, _ = precision_at_recall(floor_pa, TARGET_RECALL)

        results.append({
            "test_month": test_month,
            "n_train": len(train_idx),
            "n_test": len(test_idx),
            "n_test_positives": int(y_test.sum()),
            "chosen_cell": chosen_cell,
            "model_precision": model_prec,
            "floor_precision": floor_prec,
        })

    if not results:
        return {"error": "no valid monthly folds"}

    def _mean(key: str) -> float:
        vals = [r[key] for r in results if r[key] is not None]
        return float(np.mean(vals)) if vals else math.nan

    return {
        "monthly": results,
        "mean_model_precision": _mean("model_precision"),
        "mean_floor_precision": _mean("floor_precision"),
    }


# -------- rendering ----------------------------------------------------


def fmt(x, p=4):
    if x is None or (isinstance(x, float) and (math.isnan(x) or math.isinf(x))):
        return "—"
    return f"{x:.{p}f}"


def cell_repr(cell: dict) -> str:
    return (f"md={cell['max_depth']}, lr={cell['learning_rate']}, "
            f"n={cell['n_estimators']}, leaf={cell['min_samples_leaf']}")


def render(primary: dict, sensitivity: dict, n_dropped: int) -> str:
    out = []
    out.append(f"# Readiness Redesign — Phase 1 GBM Signal-Probe on `{TARGET_KIND}`")
    out.append("")
    out.append("Auto-generated by `analysis/phase7_chronic_label_gbm_probe/probe.py`. Re-run any time.")
    out.append("")
    out.append("## Probe contract (frozen before run)")
    out.append("")
    out.append("- **Target**: `chronic_load / chronic_label` only — chosen because the linear feasibility (PR #103) showed a real AUC gap over `event_base_rate` (0.936 vs 0.794) that did not translate to a `precision@R=0.5` win. This is the only Phase 1 target where non-linearity is a testable hypothesis rather than generic ML-hopium.")
    out.append("- **Features**: own-features from the Chronic Load writer (10 keys). No cross-sub_score features.")
    out.append(f"- **Grid**: {len(GBM_GRID)} cells, frozen before run: max_depth in (2, 3); learning_rate in (0.03, 0.1); n_estimators in (50, 100); min_samples_leaf in (5, 10). `random_state={GBM_RANDOM_STATE}`.")
    out.append("- **Hyperparameter selection**: inner train/val (chronological 80/20 inside the train period). Chosen cell refit on full train and scored ONCE on test.")
    out.append(f"- **Primary criterion**: model precision@R={TARGET_RECALL} LOWER CI > floor precision@R={TARGET_RECALL} UPPER CI. Same rule as the linear feasibility scripts.")
    out.append(f"- **Secondary metrics (interpretation only)**: precision@R={SECONDARY_RECALL}, top-k precision for k in {TOP_K_LIST}, AUC.")
    out.append(f"- **Floor**: `event_base_rate` on the same test rows; bootstrap is stratified ({BOOTSTRAP_ITERATIONS} iters).")
    out.append("- **Stop rule**: if primary criterion does not clear, **no GBM on chronic_acute_density or acute_risk** — those targets had weaker linear AUC lift, the non-linear hypothesis is least supported. Phase 1 closes on naive baselines.")
    out.append("")

    if "error" in primary:
        out.append("## Primary 70/30 split — could not run")
        out.append("")
        out.append("```")
        out.append(primary["error"])
        out.append("```")
        return "\n".join(out)

    out.append("## Primary 70/30 split")
    out.append("")
    out.append(f"- Total eligible rows: {primary['n_total']} (after dropping {n_dropped} with missing features)")
    out.append(f"- Train: {primary['n_train']} rows, {primary['train_range'][0]} → {primary['train_range'][1]}")
    dropped = primary.get('n_test_dropped_for_floor', 0)
    drop_suffix = f" (dropped {dropped} rows missing event_base_rate so model and floor share identical rows)" if dropped else ""
    out.append(f"- Test:  {primary['n_test']} rows, {primary['test_range'][0]} → {primary['test_range'][1]}{drop_suffix}")
    out.append(f"- Test positives: {primary['test_positives']} ({fmt(primary['test_base_rate'], 3)} base rate)")
    out.append("")
    out.append("### Inner train/val grid search")
    out.append("")
    out.append(f"Train period split chronologically 80/20: train' = {primary['inner_train_size']} rows, val = {primary['inner_val_size']} rows. Best cell on val by `precision@R={TARGET_RECALL}` (ties broken by val AUC):")
    out.append("")
    out.append("| max_depth | learning_rate | n_estimators | min_samples_leaf | val precision@R=0.5 | val AUC | chosen |")
    out.append("|---|---|---|---|---|---|---|")
    for cell, prec, a in primary["cell_val"]:
        marker = " ✓" if cell == primary["chosen_cell"] else ""
        out.append(
            f"| {cell['max_depth']} | {cell['learning_rate']} | {cell['n_estimators']} | "
            f"{cell['min_samples_leaf']} | {fmt(prec, 3)} | {fmt(a, 3)} |{marker} |"
        )
    out.append("")

    out.append("### Model vs floor on primary test (single evaluation)")
    out.append("")
    out.append("| | precision@R=0.5 | 95% stratified bootstrap CI | lift over base rate | AUC | precision@R=0.25 | top-5 / top-10 / top-20 | captured pos |")
    out.append("|---|---|---|---|---|---|---|---|")
    fl = primary["floor"]
    out.append(
        f"| `event_base_rate` floor | "
        f"{fmt(fl['precision'], 3)} | "
        f"[{fmt(fl['ci'][0], 3)}, {fmt(fl['ci'][1], 3)}] | "
        f"{fmt(fl['lift'], 2)} | "
        f"{fmt(fl['auc'], 3)} | "
        f"{fmt(fl['precision_secondary'], 3)} | "
        f"{fmt(fl['topk'][5], 3)} / {fmt(fl['topk'][10], 3)} / {fmt(fl['topk'][20], 3)} | "
        f"{fl['captured']} |"
    )
    md = primary["model"]
    out.append(
        f"| GBM ({cell_repr(primary['chosen_cell'])}) | "
        f"{fmt(md['precision'], 3)} | "
        f"[{fmt(md['ci'][0], 3)}, {fmt(md['ci'][1], 3)}] | "
        f"{fmt(md['lift'], 2)} | "
        f"{fmt(md['auc'], 3)} | "
        f"{fmt(md['precision_secondary'], 3)} | "
        f"{fmt(md['topk'][5], 3)} / {fmt(md['topk'][10], 3)} / {fmt(md['topk'][20], 3)} | "
        f"{md['captured']} |"
    )
    out.append("")
    out.append("### Decision")
    out.append("")
    model_lo = md["ci"][0]
    model_hi = md["ci"][1]
    model_p = md["precision"]
    floor_lo = fl["ci"][0]
    floor_hi = fl["ci"][1]
    floor_p = fl["precision"]
    if model_p is None or floor_p is None:
        out.append("**Verdict: insufficient evidence.** precision@R=0.5 is undefined on at least one of model or floor — too few positives in the test slice.")
    elif math.isnan(model_lo) or math.isnan(floor_hi):
        out.append("**Verdict: CI bounds undefined.** Stratified bootstrap could not produce stable intervals. Treat point estimates as descriptive only.")
    elif model_lo > floor_hi:
        out.append(
            f"**Verdict: GBM beats the floor with statistical significance.** "
            f"Model precision@R=0.5 lower CI ({fmt(model_lo, 3)}) exceeds floor upper CI ({fmt(floor_hi, 3)}). Intervals do not overlap. "
            f"**This is a Phase 1 candidate** — first non-naive layer to clear the predeclared bar. Next step: operationalisation review (calibration, latency, fairness across epochs), not more model search."
        )
    elif model_hi < floor_lo:
        out.append(
            f"**Verdict: GBM significantly worse than floor; no production model.** "
            f"Model CI [{fmt(model_lo, 3)}, {fmt(model_hi, 3)}] lies entirely below floor CI [{fmt(floor_lo, 3)}, {fmt(floor_hi, 3)}]. "
            f"Per the stop rule, Phase 1 closes here — no GBM on chronic_acute_density or acute_risk. `event_base_rate` is the deployable layer."
        )
    else:
        out.append(
            f"**Verdict: no production model — Phase 1 closes on the naive layer.** "
            f"Model precision@R=0.5 = {fmt(model_p, 3)} (CI [{fmt(model_lo, 3)}, {fmt(model_hi, 3)}]) vs floor {fmt(floor_p, 3)} (CI [{fmt(floor_lo, 3)}, {fmt(floor_hi, 3)}]). "
            f"CIs overlap; the model does not clear the predeclared criterion. "
            f"Per the stop rule fixed before the run: **no GBM on chronic_acute_density or acute_risk**. `event_base_rate` is the Phase 1 production layer for all classifier targets."
        )
    out.append("")
    out.append(
        "**Reading the secondary metrics.** They are reported for interpretation, not for switching the verdict. "
        "If AUC + top-k + precision@R=0.25 all clearly favour the GBM while precision@R=0.5 does not, that's evidence the ranking signal is real but the predeclared operating point is the wrong one — a candidate for a Phase 2 conversation about operating-point choice, not a back-door win for this probe."
    )
    out.append("")

    if "error" in sensitivity:
        out.append("## Sensitivity — could not run")
        out.append(sensitivity["error"])
    else:
        out.append("## Sensitivity — expanding walk-forward, monthly blocks")
        out.append("")
        out.append("Hyperparameters selected per month via inner train/val on the cumulative-train window (never on the held-out month). Floor precision@R=0.5 reported on the same month rows so the columns are directly comparable.")
        out.append("")
        out.append("| test_month | n_train | n_test | n_pos | chosen cell | model precision@R=0.5 | floor precision@R=0.5 |")
        out.append("|---|---|---|---|---|---|---|")
        for r in sensitivity["monthly"]:
            out.append(
                f"| {r['test_month']} | {r['n_train']} | {r['n_test']} | {r['n_test_positives']} | "
                f"{cell_repr(r['chosen_cell'])} | "
                f"{fmt(r['model_precision'], 3)} | {fmt(r['floor_precision'], 3)} |"
            )
        out.append("")
        out.append(f"Mean across monthly tests — model: {fmt(sensitivity['mean_model_precision'], 3)}, floor: {fmt(sensitivity['mean_floor_precision'], 3)}.")
        out.append("")
        out.append("Materially different from the primary split would indicate the 70/30 caught an unusually favourable/unfavourable test tail.")
    return "\n".join(out)


# -------- main ---------------------------------------------------------


def main():
    rows = load_rows()
    if not rows:
        print(f"# GBM probe — no eligible rows on source_2025_current; cannot run.")
        return
    baseline_map = load_event_base_rate()
    _, _, dates = to_matrix(rows)
    n_dropped = len(rows) - len(dates)
    primary = primary_split_and_evaluate(rows, baseline_map)
    sensitivity = walk_forward(rows, baseline_map)
    print(render(primary, sensitivity, n_dropped))


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as e:
        print(f"# GBM probe — psql error\n\n{e.stderr}", file=sys.stderr)
        sys.exit(1)
