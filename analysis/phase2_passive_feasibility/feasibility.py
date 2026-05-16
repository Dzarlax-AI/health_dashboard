#!/usr/bin/env python3
"""Phase 1 step 2 — Passive Efficiency rolling_3d model feasibility.

Question this script answers: does a linear/regularized model over the
Phase 0 Passive own-features beat the `ewma_45d` naive floor on the
`source_2025_current` test slice, measured by lower 95% bootstrap CI
bound on MAE?

Strict scope, agreed before writing:
- target: `passive_efficiency / rolling_3d`
- test slice: `source_2025_current` only
- features: Passive own-features from `feature_snapshots` (no
  cross-sub_score features yet — that's a fallback if linear fails)
- models: OLS + Ridge over an alpha grid; no Lasso, no trees
- split: chronological 70/30 primary + expanding walk-forward monthly
  sensitivity
- bootstrap: block bootstrap with 14-day contiguous blocks (NOT
  shuffled — time-series uncertainty)
- success criterion: model MAE on primary test must beat
  ewma_45d LOWER CI bound (~2.93 bpm), not the point estimate
- deps: numpy only; sklearn intentionally avoided

No production code touched. One offline script + one markdown report.

## Running

    PSQL=/path/to/psql.exe PYTHONUTF8=1 python \\
        analysis/phase2_passive_feasibility/feasibility.py \\
        > READINESS_REDESIGN_PHASE1_PASSIVE_FEASIBILITY.md

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

# Floor from PR #96/#98: ewma_45d on Passive rolling_3d, source_2025_current
EWMA45_FLOOR_MAE = 3.1911
EWMA45_FLOOR_CI_LO = 2.9263
EWMA45_FLOOR_CI_HI = 3.4626

# Feature keys from internal/storage/passive_efficiency_writer.go.
# All Passive own-features — no cross-sub_score signals (first iteration).
PASSIVE_FEATURE_KEYS = [
    "prev_walking_hr",
    "walking_hr_mean_7d",
    "walking_hr_mean_30d",
    "walking_hr_ewma_45d",
    "walking_hr_ewma_180d",
    "walking_hr_delta_vs_ewma_45d",
    "walking_hr_delta_vs_ewma_180d",
    "eligible_count_7d",
    "eligible_count_45d",
    "eligible_count_180d",
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
    """Load eligible target + features for Passive rolling_3d, test slice,
    source_2025_current epoch only."""
    sql = f"""
        SELECT t.date,
               t.target_value,
               f.features::text
          FROM target_snapshots t
          JOIN feature_snapshots f
            ON f.date = t.date AND f.sub_score = t.sub_score
         WHERE t.sub_score = 'passive_efficiency'
           AND t.target_kind = 'rolling_3d'
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
        for key in PASSIVE_FEATURE_KEYS:
            v = feats_raw.get(key)
            if v is None:
                feats[key] = None
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
        if all(r.features[k] is not None for k in PASSIVE_FEATURE_KEYS):
            keep.append(r)
    if not keep:
        return np.zeros((0, len(PASSIVE_FEATURE_KEYS))), np.zeros((0,)), []
    X = np.array(
        [[float(r.features[k]) for k in PASSIVE_FEATURE_KEYS] for r in keep],
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
    """Block bootstrap MAE 95% CI. Resamples contiguous date-aligned
    blocks (default 14 days) with replacement until the sample length
    matches the original, then recomputes MAE.

    This preserves autocorrelation that a shuffled bootstrap would
    destroy — Passive walking_hr is autocorrelated (3-day rolling
    average), so shuffled CIs would understate uncertainty.
    """
    if residuals.size == 0 or not dates:
        return math.nan, math.nan
    n = residuals.size
    # Build block start positions: every `block_days` along the date axis.
    # Since `dates` is sorted ascending, slice by index window of size
    # equal to the number of rows in a block. Block size in rows varies
    # slightly because the dates are not perfectly contiguous (some days
    # missing), so we use index windows of width `block_days` rows as a
    # reasonable approximation.
    rng = random.Random(seed)
    starts = list(range(0, n, block_days))
    results = []
    for _ in range(iterations):
        sample: list[float] = []
        while len(sample) < n:
            s = rng.choice(starts)
            block = residuals[s : s + block_days]
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
    if cut < 5 or n - cut < 5:
        return {"error": f"not enough rows for primary split: n={n}, cut={cut}"}

    train_idx = slice(0, cut)
    test_idx = slice(cut, n)
    X_train, y_train = X[train_idx], y[train_idx]
    X_test, y_test = X[test_idx], y[test_idx]
    train_dates = dates[:cut]
    test_dates = dates[cut:]

    X_train_s, X_test_s, _, _ = standardize_train_apply(X_train, X_test)

    # OLS.
    ols_coef = fit_ols(X_train_s, y_train)
    ols_pred = predict(ols_coef, X_test_s)
    ols_resid = y_test - ols_pred
    ols_mae = mae(ols_resid)
    ols_ci = block_bootstrap_mae(ols_resid, test_dates)

    # Ridge over alpha grid.
    ridge_runs = []
    for alpha in RIDGE_ALPHAS:
        coef = fit_ridge(X_train_s, y_train, alpha)
        pred = predict(coef, X_test_s)
        resid = y_test - pred
        m = mae(resid)
        ridge_runs.append({"alpha": alpha, "mae": m, "residuals": resid})

    best_ridge = min(ridge_runs, key=lambda r: r["mae"])
    best_ridge_ci = block_bootstrap_mae(best_ridge["residuals"], test_dates)

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
        "ols": {"mae": ols_mae, "ci": ols_ci},
        "ridge_runs": [
            {"alpha": r["alpha"], "mae": r["mae"]} for r in ridge_runs
        ],
        "best_ridge": {
            "alpha": best_ridge["alpha"],
            "mae": best_ridge["mae"],
            "ci": best_ridge_ci,
        },
        "naive_ewma_45d_on_same_split": {"mae": naive_mae, "ci": naive_ci},
    }


def load_baseline_predictions(baseline_kind: str) -> dict[date, float]:
    sql = f"""
        SELECT date, predicted_value
          FROM naive_baselines
         WHERE sub_score = 'passive_efficiency'
           AND target_kind = 'rolling_3d'
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
    out.append("# Readiness Redesign — Phase 1 Passive `rolling_3d` Model Feasibility")
    out.append("")
    out.append("Auto-generated by `analysis/phase2_passive_feasibility/feasibility.py`. Re-run any time.")
    out.append("")
    out.append("## Methodology")
    out.append("")
    out.append(f"- **Target**: `passive_efficiency / rolling_3d`")
    out.append(f"- **Test slice**: `source_2025_current` only (`{TEST_START}` → `{TEST_END}`)")
    out.append(f"- **Features**: Passive own-features from `feature_snapshots` (no cross-sub_score signals on this iteration).")
    out.append(f"- **Models**: OLS + Ridge over alpha grid {RIDGE_ALPHAS}. No Lasso, no trees.")
    out.append(f"- **Primary split**: chronological {int(PRIMARY_SPLIT_RATIO*100)}/{100-int(PRIMARY_SPLIT_RATIO*100)}.")
    out.append(f"- **Sensitivity**: expanding walk-forward monthly blocks.")
    out.append(f"- **Bootstrap**: block bootstrap with {BLOCK_BOOTSTRAP_BLOCK_DAYS}-day contiguous blocks, {BOOTSTRAP_ITERATIONS} iterations. Preserves autocorrelation that a shuffled bootstrap would destroy.")
    out.append(f"- **Standardisation**: per-feature z-score, fitted on train and applied to test (no leakage).")
    out.append("")
    out.append(f"- **Floor to beat**: `ewma_45d` MAE point {EWMA45_FLOOR_MAE:.4f} bpm, **lower CI bound {EWMA45_FLOOR_CI_LO:.4f}** (from floors report on full test slice).")
    out.append(f"- **Success criterion**: model MAE on primary test must beat **{EWMA45_FLOOR_CI_LO:.4f} bpm** — the lower CI bound, NOT the point estimate. Beating the point estimate but not the CI lower bound is statistical noise.")
    out.append("")

    if "error" in primary:
        out.append(f"## Primary 70/30 split — could not run")
        out.append("")
        out.append(f"```")
        out.append(primary["error"])
        out.append(f"```")
        return "\n".join(out)

    out.append("## Primary 70/30 split")
    out.append("")
    out.append(f"- Total rows: {primary['n_total']} (after dropping {n_dropped} rows with missing features)")
    out.append(f"- Train: {primary['n_train']} rows, {primary['train_range'][0]} → {primary['train_range'][1]}")
    out.append(f"- Test:  {primary['n_test']} rows, {primary['test_range'][0]} → {primary['test_range'][1]}")
    out.append("")
    out.append("### Model results on test")
    out.append("")
    out.append("| model | MAE (bpm) | 95% block-bootstrap CI |")
    out.append("|---|---|---|")
    out.append(f"| EWMA45 baseline (on same split) | {fmt(primary['naive_ewma_45d_on_same_split']['mae'])} | [{fmt(primary['naive_ewma_45d_on_same_split']['ci'][0])}, {fmt(primary['naive_ewma_45d_on_same_split']['ci'][1])}] |")
    out.append(f"| OLS | {fmt(primary['ols']['mae'])} | [{fmt(primary['ols']['ci'][0])}, {fmt(primary['ols']['ci'][1])}] |")
    for r in primary['ridge_runs']:
        marker = " (best)" if r['alpha'] == primary['best_ridge']['alpha'] else ""
        out.append(f"| Ridge α={r['alpha']}{marker} | {fmt(r['mae'])} | — |")
    out.append(f"| Ridge α={primary['best_ridge']['alpha']} (best, with CI) | {fmt(primary['best_ridge']['mae'])} | [{fmt(primary['best_ridge']['ci'][0])}, {fmt(primary['best_ridge']['ci'][1])}] |")
    out.append("")
    out.append("### Decision")
    out.append("")
    best_model_mae = min(primary['ols']['mae'], primary['best_ridge']['mae'])
    best_model_name = "OLS" if primary['ols']['mae'] <= primary['best_ridge']['mae'] else f"Ridge α={primary['best_ridge']['alpha']}"
    best_model_ci_hi = primary['ols']['ci'][1] if best_model_name == "OLS" else primary['best_ridge']['ci'][1]

    out.append(f"Best linear model on primary test: **{best_model_name}** with MAE = {best_model_mae:.4f} bpm, upper CI = {best_model_ci_hi:.4f}.")
    out.append("")
    out.append(f"Floor to beat: **{EWMA45_FLOOR_CI_LO:.4f} bpm** (lower bound of EWMA45 CI on the full test slice).")
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
            f"(Recovery rolling_3d, sleep_debt_7d, sustained_hr_load) added to the feature set; "
            f"alternate target encoding; or accept that EWMA45 is the production layer for Passive "
            f"rolling_3d and shift focus to other sub_scores."
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
        out.append(f"Across all monthly tests:")
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
        print("# Phase 1 Passive Feasibility — no eligible rows on source_2025_current; cannot run.")
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
        print(f"# Phase 1 Passive Feasibility — psql error\n\n{e.stderr}", file=sys.stderr)
        sys.exit(1)
