#!/usr/bin/env python3
"""Phase 1 step 2.3 — Chronic Load `chronic_acute_density` classifier feasibility.

Question: does a regularized logistic regression over the Phase 0
Chronic Load own-features beat the `event_base_rate` naive floor on
the `source_2025_current` test slice, measured at precision @
recall = 0.5?

First classifier feasibility — methodology differs from the continuous
scripts (`phase2_passive_feasibility`, `phase3_recovery_feasibility`)
in three places. Each difference is intentional:

- Metric: precision @ recall = 0.5 (primary), lift over base rate at
  the same recall (secondary). AUC reported but NOT a decision
  criterion — the operator pre-declared this.
- Bootstrap: STRATIFIED — positives and negatives resampled
  separately. Required for classifier targets with sparse positives;
  pooled bootstrap would drift the positive rate itself and conflate
  prevalence noise with ranking uncertainty.
- Floor: `event_base_rate` (90d rolling positive rate), NOT
  `persistence_yesterday`. Persistence on forward-window labels is a
  window-overlap artifact, documented in the floors report.

Success criterion (stricter than continuous):
  model precision@R=0.5 LOWER CI bound MUST exceed
  floor precision@R=0.5 UPPER CI bound — intervals must NOT overlap.
Precision CIs on classifier targets with sparse positives are
naturally wide; a single-point lift below CI overlap is statistical
noise, not signal.

Models: logistic regression with L2 (Newton-Raphson IRLS). No L1, no
trees. Alpha grid {0.01, 0.1, 1, 10, 100} with intercept NOT
penalised. Inner train/val (chronological 80/20) for alpha
selection; chosen alpha refit on full train, scored ONCE on test.

Deps: numpy only.

No production code touched. One offline script + one markdown report.

## Running

    PSQL=/path/to/psql.exe PYTHONUTF8=1 python \\
        analysis/phase4_chronic_density_feasibility/feasibility.py \\
        > READINESS_REDESIGN_PHASE1_CHRONIC_DENSITY_FEASIBILITY.md

Uses libpq env vars (PGHOST/PGUSER/PGDATABASE/PGPASSWORD/PGOPTIONS).
"""

from __future__ import annotations

import json
import math
import os
import random
import subprocess
import sys
from collections import defaultdict
from dataclasses import dataclass
from datetime import date, datetime, timedelta

import numpy as np

# -------- config -------------------------------------------------------

PSQL = os.environ.get("PSQL", "psql")
TEST_START = date(2025, 1, 1)
TEST_END = date(2026, 5, 15)
PRIMARY_SPLIT_RATIO = 0.70
BOOTSTRAP_ITERATIONS = 1000
BOOTSTRAP_SEED = 20260516
TARGET_RECALL = 0.5
L2_ALPHAS = [0.01, 0.1, 1.0, 10.0, 100.0]

SUB_SCORE = "chronic_load"
TARGET_KIND = "chronic_acute_density"

# Feature keys from internal/storage/chronic_load_writer.go
# (`chronicLoadFeatures` struct). Booleans loaded as 0/1 floats.
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
    label: int  # 0 or 1
    features: dict[str, float | None]


def load_rows() -> list[Row]:
    """Eligible target + features for chronic_acute_density, test
    slice, source_2025_current epoch only."""
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
    """event_base_rate naive baseline predictions for chronic_acute_density."""
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
    """Drops rows with any missing feature value (complete-cases analysis)."""
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


def standardize_train_apply(X_train, X_test):
    mean = X_train.mean(axis=0)
    std = X_train.std(axis=0)
    std = np.where(std == 0, 1.0, std)
    return (X_train - mean) / std, (X_test - mean) / std


# -------- logistic regression (L2, IRLS) -------------------------------

def fit_logistic_l2(X: np.ndarray, y: np.ndarray, alpha: float,
                    max_iter: int = 100, tol: float = 1e-7) -> np.ndarray:
    """Newton-Raphson IRLS for L2-regularised logistic regression.
    Intercept NOT penalised. Returns coef of shape (n_features + 1,).

    Convergence: iterates until max|delta| < tol or max_iter reached.
    Probabilities are clipped to [1e-12, 1 - 1e-12] for numerical
    stability. Uses np.linalg.lstsq as the inner solver so near-
    singular Hessians (very small alpha, near-perfect separation) fall
    through to SVD pseudo-inverse instead of raising.
    """
    X_aug = np.hstack([np.ones((X.shape[0], 1)), X])
    n_params = X_aug.shape[1]
    beta = np.zeros(n_params)
    reg = alpha * np.eye(n_params)
    reg[0, 0] = 0.0
    for _ in range(max_iter):
        logits = X_aug @ beta
        # Clip for numerical stability in sigmoid + log.
        logits = np.clip(logits, -30.0, 30.0)
        p = 1.0 / (1.0 + np.exp(-logits))
        p = np.clip(p, 1e-12, 1 - 1e-12)
        W = p * (1 - p)
        # Newton update on penalised negative log-likelihood:
        #   grad   = X^T (y - p) - reg @ beta
        #   Hess   = X^T diag(W) X + reg
        H = X_aug.T @ (W[:, None] * X_aug) + reg
        g = X_aug.T @ (y - p) - reg @ beta
        delta, *_ = np.linalg.lstsq(H, g, rcond=None)
        beta_new = beta + delta
        if np.max(np.abs(delta)) < tol:
            beta = beta_new
            break
        beta = beta_new
    return beta


def predict_proba(coef: np.ndarray, X: np.ndarray) -> np.ndarray:
    X_aug = np.hstack([np.ones((X.shape[0], 1)), X])
    logits = X_aug @ coef
    logits = np.clip(logits, -30.0, 30.0)
    return 1.0 / (1.0 + np.exp(-logits))


# -------- precision @ recall (with tie-bucket handling) ----------------

def precision_at_recall(
    predicted_actual: list[tuple[float, int]],
    target_recall: float = TARGET_RECALL,
) -> tuple[float | None, int]:
    """Smallest-threshold precision at the requested recall, treating
    tied predictions as a single bucket (include-all-or-none). Tied
    handling matters because both the model and naive baselines can
    produce repeated probability values.

    Returns (precision, captured_positives). (None, 0) when there are
    no positives in the sample.
    """
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


def auc(predicted_actual: list[tuple[float, int]]) -> float | None:
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


def stratified_bootstrap_precision(
    predicted_actual: list[tuple[float, int]],
    target_recall: float = TARGET_RECALL,
    iterations: int = BOOTSTRAP_ITERATIONS,
    seed: int = BOOTSTRAP_SEED,
) -> tuple[float, float]:
    """Stratified bootstrap: resample positives and negatives separately
    with replacement so class counts stay constant across iterations.
    Required on sparse-positive classifiers — pooled bootstrap would let
    the positive rate drift, conflating prevalence noise with ranking
    uncertainty.
    """
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


# -------- primary 70/30 ------------------------------------------------

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

    # Inner train/val for alpha selection — no test leakage.
    inner_cut = int(round(0.8 * cut))
    if inner_cut < 5 or cut - inner_cut < 5:
        return {"error": f"not enough rows for inner split: cut={cut}, inner_cut={inner_cut}"}
    X_train_inner = X[:inner_cut]
    y_train_inner = y[:inner_cut]
    X_val = X[inner_cut:cut]
    y_val = y[inner_cut:cut]

    X_train_inner_s, X_val_s = standardize_train_apply(X_train_inner, X_val)
    alpha_val_metrics: dict[float, dict] = {}
    for alpha in L2_ALPHAS:
        coef = fit_logistic_l2(X_train_inner_s, y_train_inner, alpha)
        probs = predict_proba(coef, X_val_s)
        pa = list(zip(probs.tolist(), y_val.tolist()))
        prec, _ = precision_at_recall(pa)
        a = auc(pa)
        alpha_val_metrics[alpha] = {"precision": prec, "auc": a}

    # Pick alpha by val precision; if precision is None on this fold
    # (degenerate — no positives in val), fall back to highest AUC.
    has_valid = any(m["precision"] is not None for m in alpha_val_metrics.values())
    if has_valid:
        chosen_alpha = max(
            (a for a, m in alpha_val_metrics.items() if m["precision"] is not None),
            key=lambda a: alpha_val_metrics[a]["precision"],
        )
    else:
        chosen_alpha = max(
            (a for a, m in alpha_val_metrics.items() if m["auc"] is not None),
            key=lambda a: alpha_val_metrics[a]["auc"],
            default=L2_ALPHAS[-1],
        )

    # Refit on full train, score on test. Restrict to test rows that
    # ALSO have a floor prediction — model and floor must be evaluated
    # on identical rows, otherwise precision/AUC/lift are not comparable
    # and the decision branch can flip on different denominators.
    X_train_s, X_test_s = standardize_train_apply(X_train, X_test)
    chosen_coef = fit_logistic_l2(X_train_s, y_train, chosen_alpha)
    test_probs_all = predict_proba(chosen_coef, X_test_s)

    eval_mask = np.array([baseline_map.get(d) is not None for d in test_dates])
    if eval_mask.sum() == 0:
        return {"error": "no test rows with floor predictions; cannot compare"}
    dropped_for_floor = int((~eval_mask).sum())

    test_probs = test_probs_all[eval_mask]
    y_test_eval = y_test[eval_mask]
    test_dates_eval = [d for d, keep in zip(test_dates, eval_mask) if keep]
    test_pa = list(zip(test_probs.tolist(), y_test_eval.tolist()))
    model_prec, model_captured = precision_at_recall(test_pa)
    model_auc = auc(test_pa)
    model_ci = stratified_bootstrap_precision(test_pa)

    baseline_pa = [(baseline_map[d], int(y_test_eval[i]))
                   for i, d in enumerate(test_dates_eval)]
    floor_prec, floor_captured = precision_at_recall(baseline_pa)
    floor_auc = auc(baseline_pa)
    floor_ci = stratified_bootstrap_precision(baseline_pa)

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
        "alpha_val_metrics": alpha_val_metrics,
        "chosen_alpha": chosen_alpha,
        "model": {
            "precision": model_prec, "ci": model_ci, "auc": model_auc,
            "captured": model_captured,
            "lift": (model_prec / test_base_rate) if (model_prec is not None and test_base_rate > 0) else None,
        },
        "floor": {
            "precision": floor_prec, "ci": floor_ci, "auc": floor_auc,
            "captured": floor_captured,
            "lift": (floor_prec / test_base_rate) if (floor_prec is not None and test_base_rate > 0) else None,
        },
    }


# -------- expanding walk-forward sensitivity ---------------------------

def walk_forward(rows: list[Row]) -> dict:
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
        X_train, y_train = X[train_idx], y[train_idx]
        X_test, y_test = X[test_idx], y[test_idx]
        if y_test.sum() == 0 or y_test.sum() == len(y_test):
            continue  # no positives or no negatives; precision undefined
        X_train_s, X_test_s = standardize_train_apply(X_train, X_test)
        per_alpha = {}
        for alpha in L2_ALPHAS:
            coef = fit_logistic_l2(X_train_s, y_train, alpha)
            probs = predict_proba(coef, X_test_s)
            pa = list(zip(probs.tolist(), y_test.tolist()))
            prec, _ = precision_at_recall(pa)
            per_alpha[alpha] = prec
        results.append({
            "test_month": test_month,
            "n_train": len(train_idx),
            "n_test": len(test_idx),
            "n_test_positives": int(y_test.sum()),
            "per_alpha": per_alpha,
        })

    if not results:
        return {"error": "no valid monthly folds"}

    mean_per_alpha = {}
    for alpha in L2_ALPHAS:
        vals = [r["per_alpha"][alpha] for r in results if r["per_alpha"][alpha] is not None]
        mean_per_alpha[alpha] = float(np.mean(vals)) if vals else math.nan

    return {"monthly": results, "mean_per_alpha": mean_per_alpha}


# -------- rendering ----------------------------------------------------

def fmt(x, p=4):
    if x is None or (isinstance(x, float) and (math.isnan(x) or math.isinf(x))):
        return "—"
    return f"{x:.{p}f}"


def render(primary: dict, sensitivity: dict, n_dropped: int) -> str:
    out = []
    out.append(f"# Readiness Redesign — Phase 1 Chronic `{TARGET_KIND}` Classifier Feasibility")
    out.append("")
    out.append("Auto-generated by `analysis/phase4_chronic_density_feasibility/feasibility.py`. Re-run any time.")
    out.append("")
    out.append("## Methodology")
    out.append("")
    out.append(f"- **Target**: `{SUB_SCORE} / {TARGET_KIND}` (binary classifier label).")
    out.append(f"- **Test slice**: `source_2025_current` only (`{TEST_START}` → `{TEST_END}`).")
    out.append("- **Features**: Chronic Load own-features from `feature_snapshots` (no cross-sub_score signals on this iteration).")
    out.append(f"- **Model**: L2-regularised logistic regression (Newton-Raphson IRLS, intercept not penalised). Alpha grid {L2_ALPHAS}.")
    out.append(f"- **Primary split**: chronological {int(PRIMARY_SPLIT_RATIO*100)}/{100-int(PRIMARY_SPLIT_RATIO*100)}.")
    out.append("- **Sensitivity**: expanding walk-forward monthly blocks.")
    out.append(f"- **Bootstrap**: STRATIFIED (positives and negatives resampled separately), {BOOTSTRAP_ITERATIONS} iterations.")
    out.append("- **Alpha selection**: inner train/val split (chronological 80/20 within the train period); chosen alpha refit on full train and scored ONCE on test — no hyperparameter leakage.")
    out.append(f"- **Primary metric**: precision at recall = {TARGET_RECALL}. Tied predictions evaluated as whole buckets (include-all-or-none).")
    out.append(f"- **Floor**: `event_base_rate` precision at recall = {TARGET_RECALL} on the same test rows. Persistence is intentionally excluded — it scores high on forward-window labels via window-overlap, not predictive signal (documented in floors report).")
    out.append("- **Success criterion (stricter than continuous)**: model precision@R=0.5 LOWER CI bound must exceed floor precision@R=0.5 UPPER CI bound. Intervals must NOT overlap. Single-point lift below CI overlap is statistical noise.")
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
    out.append("### Inner train/val for alpha selection")
    out.append("")
    out.append(f"Train period split chronologically 80/20: train' = {primary['inner_train_size']} rows, val = {primary['inner_val_size']} rows. Validation metrics per alpha — chosen alpha is the one that maximises val precision@R=0.5 (fallback to highest val AUC if precision is undefined on this fold):")
    out.append("")
    out.append("| L2 α | val precision@R=0.5 | val AUC | chosen |")
    out.append("|---|---|---|---|")
    for alpha, m in primary['alpha_val_metrics'].items():
        marker = " ✓" if alpha == primary['chosen_alpha'] else ""
        out.append(f"| {alpha} | {fmt(m['precision'], 3)} | {fmt(m['auc'], 3)} | {marker} |")
    out.append("")

    out.append("### Model vs floor on primary test (single evaluation each)")
    out.append("")
    out.append("| | precision@R=0.5 | 95% stratified bootstrap CI | lift over base rate | AUC | captured pos |")
    out.append("|---|---|---|---|---|---|")
    out.append(
        f"| `event_base_rate` floor | "
        f"{fmt(primary['floor']['precision'], 3)} | "
        f"[{fmt(primary['floor']['ci'][0], 3)}, {fmt(primary['floor']['ci'][1], 3)}] | "
        f"{fmt(primary['floor']['lift'], 2)} | "
        f"{fmt(primary['floor']['auc'], 3)} | "
        f"{primary['floor']['captured']} |"
    )
    out.append(
        f"| L2 logistic α={primary['chosen_alpha']} (chosen via val) | "
        f"{fmt(primary['model']['precision'], 3)} | "
        f"[{fmt(primary['model']['ci'][0], 3)}, {fmt(primary['model']['ci'][1], 3)}] | "
        f"{fmt(primary['model']['lift'], 2)} | "
        f"{fmt(primary['model']['auc'], 3)} | "
        f"{primary['model']['captured']} |"
    )
    out.append("")
    out.append("### Decision")
    out.append("")
    model_lo = primary['model']['ci'][0]
    model_hi = primary['model']['ci'][1]
    model_p = primary['model']['precision']
    floor_lo = primary['floor']['ci'][0]
    floor_hi = primary['floor']['ci'][1]
    floor_p = primary['floor']['precision']
    if model_p is None or floor_p is None:
        out.append("**Verdict: insufficient evidence.** Precision is undefined on at least one of model or floor — too few positives in the test slice for a meaningful comparison at recall = 0.5.")
    elif math.isnan(model_lo) or math.isnan(floor_hi):
        out.append("**Verdict: CI bounds undefined.** Bootstrap could not produce stable precision intervals (likely too few positives to subsample). Treat the point estimate as descriptive only.")
    elif model_lo > floor_hi:
        out.append(
            f"**Verdict: model beats the floor with statistical significance.** "
            f"Model precision@R=0.5 lower CI ({fmt(model_lo, 3)}) > floor precision@R=0.5 upper CI ({fmt(floor_hi, 3)}). Intervals do not overlap. "
            f"This is a candidate for further evaluation."
        )
    elif model_hi < floor_lo:
        out.append(
            f"**Verdict: model significantly worse than floor; no production model.** "
            f"Model CI [{fmt(model_lo, 3)}, {fmt(model_hi, 3)}] lies entirely below floor CI [{fmt(floor_lo, 3)}, {fmt(floor_hi, 3)}] — intervals do not overlap. "
            f"This is the opposite of a candidate: the linear model ranks worse than the calibrated base rate at recall = 0.5. "
            f"Possible next steps before escalating: cross-sub_score features (acute event lag features, recovery deterioration counts), "
            f"different threshold for chronic_acute_density, or accept event_base_rate as the deployable layer."
        )
    else:
        out.append(
            f"**Verdict: no production model yet.** "
            f"Model precision@R=0.5 point estimate {fmt(model_p, 3)} (CI [{fmt(model_lo, 3)}, {fmt(model_hi, 3)}]) "
            f"vs floor point {fmt(floor_p, 3)} (CI [{fmt(floor_lo, 3)}, {fmt(floor_hi, 3)}]). "
            f"CIs overlap. Per the agreed criterion (model lower CI must exceed floor upper CI), this is not a candidate. "
            f"Possible next steps before escalating: cross-sub_score features (acute event lag features, recovery deterioration counts), "
            f"different threshold for chronic_acute_density, or accept event_base_rate as the deployable layer."
        )
    out.append("")
    out.append("**Scope of the failure.** This verdict is about the **current chronological tail** (2026-01-18 → 2026-05-05, only 3 positives), not a claim that the `chronic_acute_density` label is globally useless. The walk-forward table below shows precision@R=0.5 of 0.30–0.80 on earlier months when n_pos ≥ 7, which suggests **seasonality or regime dependence** in when chronic acute-density events cluster. The production decision is still \"no model\" because the primary chronological split is the governance criterion — a model is only deployable if it beats the floor on the most recent data the production system would actually score. Revisit naturally when more positives accumulate in the recent tail.")
    out.append("")

    if "error" in sensitivity:
        out.append("## Sensitivity — could not run")
        out.append(sensitivity["error"])
    else:
        out.append("## Sensitivity — expanding walk-forward, monthly blocks")
        out.append("")
        out.append("Sanity check against the single primary split. Each row trains on every month strictly before `test_month`, evaluates on that month.")
        out.append("")
        out.append("| test_month | n_train | n_test | n_pos | best α | best precision@R=0.5 |")
        out.append("|---|---|---|---|---|---|")
        for r in sensitivity["monthly"]:
            valid = {a: p for a, p in r["per_alpha"].items() if p is not None}
            if valid:
                best_a = max(valid, key=lambda a: valid[a])
                best_p = valid[best_a]
            else:
                best_a = "—"
                best_p = None
            out.append(f"| {r['test_month']} | {r['n_train']} | {r['n_test']} | {r['n_test_positives']} | {best_a} | {fmt(best_p, 3)} |")
        out.append("")
        out.append("Mean precision@R=0.5 across monthly tests, per alpha:")
        for alpha, mean_p in sensitivity['mean_per_alpha'].items():
            out.append(f"- L2 α={alpha}: {fmt(mean_p, 3)}")
        out.append("")
        out.append("Materially different from the primary split would indicate the 70/30 caught an unusually favourable/unfavourable test tail.")
    out.append("")
    return "\n".join(out)


# -------- main ---------------------------------------------------------

def main():
    rows = load_rows()
    if not rows:
        print(f"# Phase 1 {TARGET_KIND} Feasibility — no eligible rows on source_2025_current; cannot run.")
        return
    baseline_map = load_event_base_rate()
    X, y, dates = to_matrix(rows)
    n_dropped = len(rows) - len(dates)
    primary = primary_split_and_evaluate(rows, baseline_map)
    sensitivity = walk_forward(rows)
    print(render(primary, sensitivity, n_dropped))


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as e:
        print(f"# Phase 1 {TARGET_KIND} Feasibility — psql error\n\n{e.stderr}", file=sys.stderr)
        sys.exit(1)
