#!/usr/bin/env python3
"""Phase 1 step 2.2 — Recovery Stability rolling_3d model feasibility.

Question this script answers: does a linear/regularized model over the
Phase 0 Recovery own-features beat the `ewma_45d` naive floor on the
`source_2025_current` test slice, measured by lower 95% bootstrap CI
bound on MAE?

Cloned from the Passive feasibility script (PR #100) with the same
methodology and locked-in constraints. Expectations are low going in:
the floors report flagged Recovery rolling_3d as "low (close to noise
floor)" with MAE/SD ratio ≈ 0.76, similar to Passive's profile. The
clone-and-edit approach was chosen over a shared helper module —
explicit duplication is correct at this stage; two consumers do not
justify a mini-framework.

Strict scope, agreed before writing:
- target: `recovery_stability / rolling_3d`
- test slice: `source_2025_current` only
- features: Recovery own-features from `feature_snapshots` (no
  cross-sub_score features in this iteration)
- models: OLS + Ridge over an alpha grid; no Lasso, no trees
- split: chronological 70/30 primary + expanding walk-forward monthly
  sensitivity
- bootstrap: block bootstrap with 14-day calendar-day contiguous blocks
  (NOT shuffled, NOT row-based — preserves time-series autocorrelation)
- alpha selection: inner train/val split (80/20 within train period),
  never on the primary test set — no hyperparameter leakage
- success criterion: model MAE on primary test must beat
  ewma_45d LOWER CI bound (0.0231), not the point estimate
- deps: numpy only; sklearn intentionally avoided

No production code touched. One offline script + one markdown report.

## Running

    PSQL=/path/to/psql.exe PYTHONUTF8=1 python \\
        analysis/phase3_recovery_feasibility/feasibility.py \\
        > READINESS_REDESIGN_PHASE1_RECOVERY_FEASIBILITY.md

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
PRIMARY_SPLIT_RATIO = 0.70  # 70% train, 30% test (chronological)
BLOCK_BOOTSTRAP_BLOCK_DAYS = 14
BOOTSTRAP_ITERATIONS = 1000
BOOTSTRAP_SEED = 20260516
RIDGE_ALPHAS = [0.01, 0.1, 1.0, 10.0, 100.0]  # alpha=0 omitted; OLS already evaluated separately

# Sub-score under test and the target_kind being modelled.
SUB_SCORE = "recovery_stability"
TARGET_KIND = "rolling_3d"
TARGET_UNIT = ""  # sleep efficiency is a unit-less ratio in [0, 1]

# Floor from PR #96/#98: ewma_45d on Recovery rolling_3d, source_2025_current.
EWMA45_FLOOR_MAE = 0.0251
EWMA45_FLOOR_CI_LO = 0.0231
EWMA45_FLOOR_CI_HI = 0.0271

# Feature keys from internal/storage/recovery_stability_writer.go
# (`recoveryFeatures` struct). All Recovery own-features — no
# cross-sub_score signals in this iteration. The two `warmup_complete_*`
# booleans are loaded as 0/1 floats.
FEATURE_KEYS = [
    "prev_efficiency",
    "sleep_eff_mean_7d",
    "sleep_eff_ewma_45d",
    "sleep_eff_ewma_180d",
    "sleep_debt_7d_hours",
    "eligible_count_7d",
    "eligible_count_45d",
    "eligible_count_180d",
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
    target: float
    features: dict[str, float | None]


def load_rows() -> list[Row]:
    """Load eligible target + features for Recovery rolling_3d, test
    slice, source_2025_current epoch only."""
    sql = f"""
        SELECT t.date,
               t.target_value,
               f.features::text
          FROM target_snapshots t
          JOIN feature_snapshots f
            ON f.date = t.date AND f.sub_score = t.sub_score
         WHERE t.sub_score = '{SUB_SCORE}'
           AND t.target_kind = '{TARGET_KIND}'
           AND t.eligible = TRUE
           AND t.source_epoch = 'source_2025_current'
           AND t.date BETWEEN '{TEST_START}' AND '{TEST_END}'
         ORDER BY t.date ASC
    """
    rows: list[Row] = []
    for r in psql(sql):
        d = datetime.strptime(r[0], "%Y-%m-%d").date()
        target = float(r[1])
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
        rows.append(Row(d, target, feats))
    return rows


# -------- feature matrix -----------------------------------------------

def to_matrix(rows: list[Row]) -> tuple[np.ndarray, np.ndarray, list[date]]:
    """Build X, y, dates. Drops rows missing any feature so primary
    analysis stays on complete observations.

    Returns (X, y, dates) where X has shape (n_rows, n_features)."""
    keep = []
    for r in rows:
        if all(r.features[k] is not None for k in FEATURE_KEYS):
            keep.append(r)
    if not keep:
        return np.zeros((0, len(FEATURE_KEYS))), np.zeros((0,)), []
    X = np.array(
        [[float(r.features[k]) for k in FEATURE_KEYS] for r in keep],
        dtype=np.float64,
    )
    y = np.array([r.target for r in keep], dtype=np.float64)
    dates = [r.date for r in keep]
    return X, y, dates


# -------- models -------------------------------------------------------

def fit_ols(X: np.ndarray, y: np.ndarray) -> np.ndarray:
    """OLS closed form including intercept column."""
    X_aug = np.hstack([np.ones((X.shape[0], 1)), X])
    coef, *_ = np.linalg.lstsq(X_aug, y, rcond=None)
    return coef


def fit_ridge(X: np.ndarray, y: np.ndarray, alpha: float) -> np.ndarray:
    """Ridge closed form with intercept NOT penalised. Uses lstsq for
    the inner solve so near-singular A matrices (small alpha + heavily
    correlated features in a short training window) fall through to the
    SVD pseudo-inverse rather than raising."""
    X_aug = np.hstack([np.ones((X.shape[0], 1)), X])
    n_params = X_aug.shape[1]
    reg = alpha * np.eye(n_params)
    reg[0, 0] = 0.0  # do not penalise intercept
    coef, *_ = np.linalg.lstsq(X_aug.T @ X_aug + reg, X_aug.T @ y, rcond=None)
    return coef


def predict(coef: np.ndarray, X: np.ndarray) -> np.ndarray:
    X_aug = np.hstack([np.ones((X.shape[0], 1)), X])
    return X_aug @ coef


def standardize_train_apply(
    X_train: np.ndarray, X_test: np.ndarray
) -> tuple[np.ndarray, np.ndarray, np.ndarray, np.ndarray]:
    """Z-score per feature on train, same params on test. Returns
    (X_train_s, X_test_s, mean, std). Constant columns get std=1 to
    avoid divide-by-zero."""
    mean = X_train.mean(axis=0)
    std = X_train.std(axis=0)
    std = np.where(std == 0, 1.0, std)
    return (X_train - mean) / std, (X_test - mean) / std, mean, std


def mae(residuals: np.ndarray) -> float:
    if residuals.size == 0:
        return math.nan
    return float(np.abs(residuals).mean())


# -------- block bootstrap ----------------------------------------------

def block_bootstrap_mae(
    residuals: np.ndarray,
    dates: list[date],
    block_days: int = BLOCK_BOOTSTRAP_BLOCK_DAYS,
    iterations: int = BOOTSTRAP_ITERATIONS,
    seed: int = BOOTSTRAP_SEED,
) -> tuple[float, float]:
    """Block bootstrap MAE 95% CI. Resamples contiguous CALENDAR-day
    blocks (default 14 days) with replacement until the sample length
    matches the original, then recomputes MAE.

    Blocks are defined in calendar-day space, not row space. Every
    row index is a potential block start; the block ends at the last
    row whose date is ≤ start_date + block_days - 1. This handles
    discontinuous date series correctly: a "14-day block" might
    contain anywhere from a few rows (sparse window) to ~14 rows
    (dense window), but always represents the same calendar span.

    This preserves autocorrelation that a shuffled bootstrap would
    destroy — Recovery rolling_3d is by construction a 3-day rolling
    average of sleep efficiency, so adjacent rows are strongly
    correlated and shuffled CIs would understate uncertainty.
    """
    if residuals.size == 0 or not dates:
        return math.nan, math.nan
    n = residuals.size
    rng = random.Random(seed)
    starts = list(range(n))
    results = []
    for _ in range(iterations):
        sample: list[float] = []
        while len(sample) < n:
            s = rng.choice(starts)
            end_date = dates[s] + timedelta(days=block_days - 1)
            e = s
            while e < n and dates[e] <= end_date:
                e += 1
            block = residuals[s:e]
            if block.size == 0:
                continue
            sample.extend(block.tolist())
        sample = sample[:n]
        results.append(float(np.abs(np.asarray(sample)).mean()))
    results.sort()
    lo = results[int(0.025 * iterations)]
    hi = results[int(0.975 * iterations)]
    return lo, hi


# -------- primary 70/30 ------------------------------------------------

def primary_split_and_evaluate(rows: list[Row]) -> dict:
    X, y, dates = to_matrix(rows)
    n = len(dates)
    cut = int(round(PRIMARY_SPLIT_RATIO * n))
    if cut < 10 or n - cut < 5:
        return {"error": f"not enough rows for primary split: n={n}, cut={cut}"}

    train_idx = slice(0, cut)
    test_idx = slice(cut, n)
    X_train, y_train = X[train_idx], y[train_idx]
    X_test, y_test = X[test_idx], y[test_idx]
    train_dates = dates[:cut]
    test_dates = dates[cut:]

    # Inner train/val split for alpha selection — Codex P2 fix.
    # Picking the Ridge alpha on the primary test set leaks evaluation
    # labels into hyperparameter selection. Instead, choose alpha
    # chronologically inside the train period: train' = first 80% of
    # train rows, val = last 20%. Refit the chosen alpha on the FULL
    # train period before evaluating once on the primary test set.
    inner_cut = int(round(0.8 * cut))
    if inner_cut < 5 or cut - inner_cut < 5:
        return {"error": f"not enough rows for inner train/val split: cut={cut}, inner_cut={inner_cut}"}
    X_train_inner = X[:inner_cut]
    y_train_inner = y[:inner_cut]
    X_val = X[inner_cut:cut]
    y_val = y[inner_cut:cut]

    # Standardise on inner train' only, apply to val.
    X_train_inner_s, X_val_s, _, _ = standardize_train_apply(X_train_inner, X_val)
    alpha_val_maes: dict[float, float] = {}
    for alpha in RIDGE_ALPHAS:
        coef = fit_ridge(X_train_inner_s, y_train_inner, alpha)
        pred = predict(coef, X_val_s)
        alpha_val_maes[alpha] = mae(y_val - pred)
    chosen_alpha = min(alpha_val_maes.keys(), key=lambda a: alpha_val_maes[a])

    # Now standardise on the FULL train period (re-fit scaler) and apply
    # to test. OLS and the chosen-alpha Ridge are scored ONCE on test.
    X_train_s, X_test_s, _, _ = standardize_train_apply(X_train, X_test)

    # OLS.
    ols_coef = fit_ols(X_train_s, y_train)
    ols_pred = predict(ols_coef, X_test_s)
    ols_resid = y_test - ols_pred
    ols_mae = mae(ols_resid)
    ols_ci = block_bootstrap_mae(ols_resid, test_dates)

    # Ridge — chosen alpha is the only one scored on test; the rest are
    # reported as a sanity context table (val MAE per alpha) but never
    # selected post-hoc.
    chosen_coef = fit_ridge(X_train_s, y_train, chosen_alpha)
    chosen_pred = predict(chosen_coef, X_test_s)
    chosen_resid = y_test - chosen_pred
    chosen_mae = mae(chosen_resid)
    chosen_ci = block_bootstrap_mae(chosen_resid, test_dates)

    # Baseline reuse: use the existing ewma_45d naive baseline already
    # in the DB for this row range so the comparison is apples-to-apples
    # against the floor in the floors report.
    baseline_pred_map = load_baseline_predictions("ewma_45d")
    paired = [(y_test[i], baseline_pred_map.get(test_dates[i]))
              for i in range(len(test_dates))]
    naive_resid = np.array(
        [a - p for (a, p) in paired if p is not None], dtype=np.float64
    )
    naive_mae = mae(naive_resid)
    naive_ci = block_bootstrap_mae(
        naive_resid,
        [d for i, d in enumerate(test_dates)
         if baseline_pred_map.get(d) is not None],
    )

    return {
        "n_total": n,
        "train_range": (train_dates[0], train_dates[-1]),
        "test_range": (test_dates[0], test_dates[-1]),
        "n_train": cut,
        "n_test": n - cut,
        "inner_train_size": inner_cut,
        "inner_val_size": cut - inner_cut,
        "alpha_val_maes": alpha_val_maes,
        "chosen_alpha": chosen_alpha,
        "ols": {"mae": ols_mae, "ci": ols_ci},
        "chosen_ridge": {
            "alpha": chosen_alpha,
            "mae": chosen_mae,
            "ci": chosen_ci,
        },
        "naive_ewma_45d_on_same_split": {"mae": naive_mae, "ci": naive_ci},
    }


def load_baseline_predictions(baseline_kind: str) -> dict[date, float]:
    sql = f"""
        SELECT date, predicted_value
          FROM naive_baselines
         WHERE sub_score = '{SUB_SCORE}'
           AND target_kind = '{TARGET_KIND}'
           AND baseline_kind = '{baseline_kind}'
           AND source_epoch = 'source_2025_current'
           AND date BETWEEN '{TEST_START}' AND '{TEST_END}'
    """
    out: dict[date, float] = {}
    for r in psql(sql):
        d = datetime.strptime(r[0], "%Y-%m-%d").date()
        if r[1] is not None:
            out[d] = float(r[1])
    return out


# -------- expanding walk-forward sensitivity --------------------------

def walk_forward(rows: list[Row]) -> dict:
    """Expanding walk-forward: train on all months strictly before
    test month T, evaluate on month T. Iterates T from the second
    available month onward (need at least one month of train history).

    For each model class (OLS, best-alpha Ridge from primary), aggregate
    MAE across all monthly tests as a sanity check against the
    primary-split point estimate.
    """
    X, y, dates = to_matrix(rows)
    if not dates:
        return {"error": "no rows"}

    # Group indices by month.
    by_month: dict[str, list[int]] = defaultdict(list)
    for i, d in enumerate(dates):
        by_month[d.strftime("%Y-%m")].append(i)
    months_sorted = sorted(by_month.keys())
    if len(months_sorted) < 3:
        return {"error": f"only {len(months_sorted)} months; need >= 3"}

    monthly_results = []
    for k in range(1, len(months_sorted)):
        test_month = months_sorted[k]
        train_idx = []
        for m in months_sorted[:k]:
            train_idx.extend(by_month[m])
        test_idx = by_month[test_month]
        if len(train_idx) < 30 or len(test_idx) < 5:
            continue  # too sparse to be meaningful
        X_train = X[train_idx]
        y_train = y[train_idx]
        X_test = X[test_idx]
        y_test = y[test_idx]
        X_train_s, X_test_s, _, _ = standardize_train_apply(X_train, X_test)

        # OLS.
        ols_coef = fit_ols(X_train_s, y_train)
        ols_pred = predict(ols_coef, X_test_s)
        ols_mae = mae(y_test - ols_pred)

        # Ridge at each alpha; pick best on this fold for context only.
        ridge_per_alpha = {}
        for alpha in RIDGE_ALPHAS:
            coef = fit_ridge(X_train_s, y_train, alpha)
            pred = predict(coef, X_test_s)
            ridge_per_alpha[alpha] = mae(y_test - pred)

        monthly_results.append({
            "test_month": test_month,
            "n_train": len(train_idx),
            "n_test": len(test_idx),
            "ols_mae": ols_mae,
            "ridge_mae_per_alpha": ridge_per_alpha,
        })

    if not monthly_results:
        return {"error": "no valid monthly folds"}

    # Aggregate.
    ols_maes = [r["ols_mae"] for r in monthly_results]
    ridge_aggregate = {
        alpha: [r["ridge_mae_per_alpha"][alpha] for r in monthly_results]
        for alpha in RIDGE_ALPHAS
    }
    return {
        "monthly": monthly_results,
        "ols_mean_mae": float(np.mean(ols_maes)),
        "ols_median_mae": float(np.median(ols_maes)),
        "ridge_mean_mae_per_alpha": {a: float(np.mean(v)) for a, v in ridge_aggregate.items()},
    }


# -------- rendering ----------------------------------------------------

def fmt(x, p=4):
    if x is None or (isinstance(x, float) and (math.isnan(x) or math.isinf(x))):
        return "—"
    return f"{x:.{p}f}"


def render(primary: dict, sensitivity: dict, n_dropped: int) -> str:
    out = []
    out.append(f"# Readiness Redesign — Phase 1 Recovery `{TARGET_KIND}` Model Feasibility")
    out.append("")
    out.append("Auto-generated by `analysis/phase3_recovery_feasibility/feasibility.py`. Re-run any time.")
    out.append("")
    out.append("## Methodology")
    out.append("")
    out.append(f"- **Target**: `{SUB_SCORE} / {TARGET_KIND}`")
    out.append(f"- **Test slice**: `source_2025_current` only (`{TEST_START}` → `{TEST_END}`)")
    out.append("- **Features**: Recovery own-features from `feature_snapshots` (no cross-sub_score signals on this iteration).")
    out.append(f"- **Models**: OLS + Ridge over alpha grid {RIDGE_ALPHAS}. No Lasso, no trees.")
    out.append(f"- **Primary split**: chronological {int(PRIMARY_SPLIT_RATIO*100)}/{100-int(PRIMARY_SPLIT_RATIO*100)}.")
    out.append("- **Sensitivity**: expanding walk-forward monthly blocks.")
    out.append(f"- **Bootstrap**: calendar-day block bootstrap with {BLOCK_BOOTSTRAP_BLOCK_DAYS}-day contiguous blocks, {BOOTSTRAP_ITERATIONS} iterations. Preserves autocorrelation that a shuffled bootstrap would destroy.")
    out.append("- **Alpha selection**: inner train/val split (chronological 80/20 within the train period); chosen alpha refit on full train and scored ONCE on test — no hyperparameter leakage.")
    out.append("- **Standardisation**: per-feature z-score, fitted on train and applied to test (no leakage).")
    out.append("")
    out.append(f"- **Floor to beat**: `ewma_45d` MAE point {EWMA45_FLOOR_MAE:.4f}, **lower CI bound {EWMA45_FLOOR_CI_LO:.4f}** (from floors report on full test slice). Sleep efficiency is a unit-less ratio in [0, 1].")
    out.append(f"- **Success criterion**: model MAE on primary test must beat **{EWMA45_FLOOR_CI_LO:.4f}** — the lower CI bound, NOT the point estimate. Beating the point estimate but not the CI lower bound is statistical noise.")
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
    out.append(f"- Total rows: {primary['n_total']} (after dropping {n_dropped} rows with missing features)")
    out.append(f"- Train: {primary['n_train']} rows, {primary['train_range'][0]} → {primary['train_range'][1]}")
    out.append(f"- Test:  {primary['n_test']} rows, {primary['test_range'][0]} → {primary['test_range'][1]}")
    out.append("")
    out.append("### Inner train/val for alpha selection (no test leakage)")
    out.append("")
    out.append(f"Train period split chronologically 80/20: train' = {primary['inner_train_size']} rows, val = {primary['inner_val_size']} rows.")
    out.append("Validation MAE per alpha — chosen alpha is the one that minimises val MAE; refit on the full train period and scored ONCE on test:")
    out.append("")
    out.append("| Ridge α | val MAE | chosen |")
    out.append("|---|---|---|")
    for alpha, val_mae in primary['alpha_val_maes'].items():
        marker = " ✓" if alpha == primary['chosen_alpha'] else ""
        out.append(f"| {alpha} | {fmt(val_mae)} | {marker} |")
    out.append("")
    out.append("### Model results on primary test (single evaluation each)")
    out.append("")
    out.append("| model | MAE | 95% block-bootstrap CI |")
    out.append("|---|---|---|")
    out.append(f"| EWMA45 baseline (on same split) | {fmt(primary['naive_ewma_45d_on_same_split']['mae'])} | [{fmt(primary['naive_ewma_45d_on_same_split']['ci'][0])}, {fmt(primary['naive_ewma_45d_on_same_split']['ci'][1])}] |")
    out.append(f"| OLS | {fmt(primary['ols']['mae'])} | [{fmt(primary['ols']['ci'][0])}, {fmt(primary['ols']['ci'][1])}] |")
    out.append(f"| Ridge α={primary['chosen_ridge']['alpha']} (chosen via val) | {fmt(primary['chosen_ridge']['mae'])} | [{fmt(primary['chosen_ridge']['ci'][0])}, {fmt(primary['chosen_ridge']['ci'][1])}] |")
    out.append("")
    out.append("### Decision")
    out.append("")
    best_model_mae = min(primary['ols']['mae'], primary['chosen_ridge']['mae'])
    best_model_name = "OLS" if primary['ols']['mae'] <= primary['chosen_ridge']['mae'] else f"Ridge α={primary['chosen_ridge']['alpha']}"
    best_model_ci_hi = primary['ols']['ci'][1] if best_model_name == "OLS" else primary['chosen_ridge']['ci'][1]

    out.append(f"Best linear model on primary test: **{best_model_name}** with MAE = {best_model_mae:.4f}, upper CI = {best_model_ci_hi:.4f}.")
    out.append("")
    out.append(f"Floor to beat: **{EWMA45_FLOOR_CI_LO:.4f}** (lower bound of EWMA45 CI on the full test slice).")
    out.append("")
    if best_model_mae < EWMA45_FLOOR_CI_LO:
        if best_model_ci_hi < EWMA45_FLOOR_CI_LO:
            verdict = (
                "**Verdict: model beats the floor with statistical significance.** "
                "Best linear model's MAE upper CI bound is below the floor's lower CI bound — "
                "the intervals do not overlap. This is a candidate for further evaluation."
            )
        else:
            verdict = (
                "**Verdict: model beats the floor point estimate but CIs overlap with the floor.** "
                "Best linear model's MAE point estimate is below the floor's lower CI, but its own CI "
                "extends back into the floor's territory. Suggestive, not conclusive — would re-run "
                "with more data or different split before treating as a real lift."
            )
    else:
        verdict = (
            f"**Verdict: no production model yet.** "
            f"Best linear model MAE ({best_model_mae:.4f}) does not beat the floor lower CI "
            f"({EWMA45_FLOOR_CI_LO:.4f}). Per the agreed criterion this is not a candidate, "
            f"even if the model's MAE happens to be below the floor's point estimate. "
            f"Possible next steps before escalating to a tree model: cross-sub_score features "
            f"(Passive rolling_3d, sustained_hr_load, intraday HR variability if available) "
            f"added to the feature set; alternate target encoding; or accept that EWMA45 is the "
            f"production layer for Recovery rolling_3d and shift focus to other sub_scores."
        )
    out.append(verdict)
    out.append("")

    if "error" in sensitivity:
        out.append("## Sensitivity — could not run")
        out.append(sensitivity["error"])
    else:
        out.append("## Sensitivity — expanding walk-forward, monthly blocks")
        out.append("")
        out.append("Sanity check against the single primary split. Each row trains on every month strictly before `test_month`, evaluates on that month.")
        out.append("")
        out.append("| test_month | n_train | n_test | OLS MAE | best Ridge MAE |")
        out.append("|---|---|---|---|---|")
        for r in sensitivity["monthly"]:
            best_ridge_mae = min(r["ridge_mae_per_alpha"].values())
            out.append(f"| {r['test_month']} | {r['n_train']} | {r['n_test']} | {fmt(r['ols_mae'])} | {fmt(best_ridge_mae)} |")
        out.append("")
        out.append("Across all monthly tests:")
        out.append(f"- OLS mean MAE: {fmt(sensitivity['ols_mean_mae'])}, median MAE: {fmt(sensitivity['ols_median_mae'])}")
        for alpha, mean_mae in sensitivity['ridge_mean_mae_per_alpha'].items():
            out.append(f"- Ridge α={alpha} mean MAE: {fmt(mean_mae)}")
        out.append("")
        out.append("If walk-forward mean MAE is materially different from the primary test MAE, the primary split caught an unusually favourable or unfavourable test tail.")

    out.append("")
    return "\n".join(out)


# -------- main ---------------------------------------------------------

def main():
    rows = load_rows()
    if not rows:
        print("# Phase 1 Recovery Feasibility — no eligible rows on source_2025_current; cannot run.")
        return

    X, y, dates = to_matrix(rows)
    n_kept = len(dates)
    n_dropped = len(rows) - n_kept

    primary = primary_split_and_evaluate(rows)
    sensitivity = walk_forward(rows)
    print(render(primary, sensitivity, n_dropped))


if __name__ == "__main__":
    try:
        main()
    except subprocess.CalledProcessError as e:
        print(f"# Phase 1 Recovery Feasibility — psql error\n\n{e.stderr}", file=sys.stderr)
        sys.exit(1)
