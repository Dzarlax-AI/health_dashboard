#!/usr/bin/env python3
"""Emit a deterministic producer inventory for every declared catalog identifier."""

import argparse
from collections import Counter
import csv
import json
import re
import sys

from check_semantic_catalog import ROOT, definitions, read_catalog
from native_catalog_vars import native_catalog_vars

PILOT = ROOT / "experiments/dbt_metrics"

# One production macro consumer is enough to establish shared computation in
# a model. This does not establish that every output column uses that macro.
SHARED = {
    "captured_dashboard_sleep_quality_v1": "DashboardSleepQualitySQL",
    "briefing_section_status_v1": "BriefingSectionsSQL",
    "hrv_fixed_window_baseline_v1": "PersonalBaselineResultSQL",
    "briefing_metric_trends_v1": "BriefingMetricTrendsSQL",
    "briefing_signals_v1": "BriefingStressSignalsSQL",
    "chip_baseline_v1": "ChipBaselineSQL",
    "chip_calibration_v1": "ChipCalibrationSQL",
    "chip_targets_v1": "ChipTargetsSQL",
    "daily_decision_boundary_v1": "DailyDecisionSQL",
    "daily_metrics": "DailyRetainedValuesSQL",
    "sleep_balance_history_v1": "SleepBalancePeriodSQL",
    "sleep_duration_balance_v1": "SleepBalanceWindowSQL",
    "stress_hour_z_v1": "HourlyStressSQL",
    "oxygen_cluster_v1": "OxygenClusterRowsSQL",
    "personal_baseline_v1": "PersonalBaselineResultSQL",
    "semantic_energy_bank_v3": "EnergyBankSQL",
    "semantic_energy_guidance_v1": "EnergyGuidanceSQL",
    "semantic_energy_legacy_v1": "EnergyLegacySQL",
    "semantic_hrv_daily_v1": "DailySourceMeanSQL",
    "semantic_readiness_score_v1": "ReadinessScoreSQL",
    "semantic_sleep_day_v1": "SleepDurationComparisonFieldsSQL",
    "semantic_sleep_duration_comparison_v1": "SleepDurationComparisonFieldsSQL",
    # The main-night quality model and the production briefing adapter expand
    # the same semantic_sleep_quality SQL macro.
    "semantic_sleep_quality_v1": "DashboardSleepQualitySQL",
    "shadow_sleep_daily_v1": "SleepDailySQL",
    "sleep_architecture_days_v1": "SleepArchitectureDaysSQL",
    "sleep_architecture_windows_v1": "SleepArchitectureWindowsSQL",
    "sleep_duration_reference_v1": "SleepDurationReferenceSQL",
    "sleep_regularity_index_v1": "SleepRegularitySQL",
    "weekly_sleep_analysis_v1": "WeeklySleepAnalysisSQL",
    "workout_period_v1": "WorkoutPeriodSQL",
    "workout_type_v1": "WorkoutTypeSQL",
}

SHADOW = set("""
semantic_hrv_after_workout_v1 semantic_hrv_night_v1 semantic_hrv_observation_v1 semantic_hrv_workout_v1
semantic_sleep_continuity_v1 semantic_sleep_history_v1
""".split())

# Record the product write entry point even when the catalog projects a frozen
# value. Reading a snapshot alone does not establish which formula produced it.
ARCHIVE_WRITERS = {
    "archived_chip_calibration_v1": ("readiness_redesign.go", "SaveChipCalibration"),
    "archived_energy_eod_v1": ("energy_backfill.go", "BackfillEnergyRange"),
    "archived_energy_snapshot_v1": ("energy_snapshot_write.go", "UpsertEnergySnapshotAt"),
    "archived_features_v1": ("readiness_redesign.go", "SaveFeatureSnapshot"),
    "archived_legacy_energy_eod_v1": ("energy_history.go", "SaveEnergyBankSnapshot"),
    "archived_main_night_v1": ("completed_night_sleep.go", "ReconcileCompletedNightSleep"),
    "archived_morning_sleep_v1": ("report_history.go", "ReserveArchivedReport"),
    "archived_naive_baseline_v1": ("readiness_redesign.go", "SaveNaiveBaseline"),
    "archived_sleep_goal_v1": ("sleep_duration_balance.go", "SaveSleepGoal"),
    "archived_target_v1": ("readiness_redesign.go", "SaveTargetSnapshot"),
    "archived_today_evidence_v1": ("daily_insight_bundles.go", "UpsertDailyInsightSnapshot"),
    "archived_wake_time_v1": ("wake_detection.go", "ComputeMorningWakeStatus"),
    "archived_workout_v1": ("workouts.go", "UpsertWorkout"),
}


CAPTURED = {"captured_briefing_fields_v1", "captured_illness_evidence_v1", "captured_readiness_component_v1"}


# Verify the worker selector, target alias, shared solver and receipt result shape.
DECISIONS = {
    "semantic_readiness_score_v1": ("hdbt_readiness_decisions", "hdbt_w_readiness", "readiness_score_v1",
                                    ("RawScore", "DisplayScore", "Confidence", "CapReason")),
    "semantic_energy_bank_v3": ("worker_energy_decisions", "hdbt_w_energy", "energy_bank_v3",
                                ("Bank", "Display", "State", "Flags", "FormulaVersion", "AlphaUsed",
                                 "TodayDrain", "TodayRestore", "Components")),
    "daily_decision_boundary_v1": ("worker_today_decisions", "hdbt_w_today", "daily_decision_boundary",
                                   ("mode", "evidence_domains", "signal_keys")),
}


def verify_worker_bindings():
    worker = (PILOT / "scripts/tenant_aggregate_worker.py").read_text()
    for model, (selector, alias, macro, fields) in DECISIONS.items():
        sql = (PILOT / "models" / (selector + ".sql")).read_text()
        graph = (PILOT / "models" / (model + ".sql")).read_text()
        if not (f'"{selector}"' in worker and "from " + alias in worker
                and f"alias='{alias}'" in sql and macro + "(" in graph and macro + "(" in sql
                and all(f"'{field}'" in sql for field in fields)):
            raise ValueError(f"worker selection, solver or output changed for {model}")


def production_calls():
    calls = {}
    for path in sorted((ROOT / "internal").rglob("*.go")):
        if path.name.endswith("_test.go"):
            continue
        for symbol in set(re.findall(r"\bdbtmetrics\.(\w+)", path.read_text())):
            calls.setdefault(symbol, []).append(str(path.relative_to(ROOT)))
    return calls


def inventory():
    verify_worker_bindings()
    catalog = read_catalog()
    native_vars = native_catalog_vars("inventory")
    native = set(native_vars["native_catalog_bindings"]) | set(native_vars["profile_graph_observations"])
    calls = production_calls()
    bindings = catalog["dbt_bindings"]
    non_values = catalog["non_values"]
    observations = set(catalog["observations"]) - non_values.keys()
    writers = {}
    for model, (filename, symbol) in ARCHIVE_WRITERS.items():
        path = ROOT / "internal/storage" / filename
        if f"func (s *DB) {symbol}(" not in path.read_text():
            raise ValueError(f"missing product writer for {model}: {symbol}")
        writers[model] = f"{path.relative_to(ROOT)}::{symbol}"
    rows = []
    ids = set()
    for spec in sorted(definitions(catalog), key=lambda row: row["metric_id"]):
        metric = spec["metric_id"]
        if metric in ids or sum(metric in group for group in (bindings, observations, non_values)) != 1:
            raise ValueError(f"ambiguous or absent producer: {metric}")
        ids.add(metric)
        model = columns = evidence = ""
        publication = "none"
        if metric in non_values:
            kind, evidence = non_values[metric]
        elif metric in observations:
            kind, evidence = "native_observation", "metric_points"
        else:
            binding = bindings[metric]
            model = binding["model"]
            columns = ",".join(binding["columns"])
            sql = (PILOT / "models" / (model + ".sql")).read_text()
            if model.startswith("archived_"):
                for macro in re.findall(r"{{\s*(archived_\w+_rows)\(\)", sql):
                    source = (PILOT / "macros" / "archived_product_rows.sql").read_text()
                    body = re.search(r"{% macro " + macro + r"\([^)]*\) %}(.*?){% endmacro %}", source, re.S)
                    if not body:
                        raise ValueError(f"missing archived projection {macro} for {metric}")
                    sql += body.group(1)
                tables = sorted(set(re.findall(r"source\(\s*'archive_records'\s*,\s*'(\w+)'", sql)))
                if not tables:
                    raise ValueError(f"no archived source table for {metric}")
                kind, evidence = "stored_projection", ",".join(tables)
                if model not in writers:
                    raise ValueError(f"missing primary writer mapping for {model}")
            elif model in CAPTURED:
                kind, evidence = "decision_capture_projection", "today_metric_captures_v1"
                if evidence not in sql:
                    raise ValueError(f"capture missing from {model}")
            elif model in SHARED:
                symbol = SHARED[model]
                kind = "model_with_shared_runtime_sql"
                evidence = symbol + "@" + ",".join(calls[symbol])
            elif model in SHADOW:
                kind, evidence = "graph_model_without_shared_runtime_call", "models/" + model + ".sql"
            else:
                raise ValueError(f"classify the new producer {model}")
            if model == "daily_metrics":
                publication = "tenant_daily_worker"
            elif model in DECISIONS:
                publication = "decision_worker_family"
        rows.append(dict(metric_id=metric, catalog_owner=spec.get("owner", ""),
                         source_kind=kind, model=model, columns=columns, snapshot_writer=writers.get(model, ""),
                         evidence=evidence, publication_path=publication, tenant_native=metric in native, tenant_disposition=metric in non_values,
                         additional_wake_replay="wake_confirmation_v1" if model == "archived_wake_time_v1" else "",
                         additional_feature_replay="native_feature_replay_v1" if model == "archived_features_v1" else ""))
    if ids != set(bindings) | observations | set(non_values):
        raise ValueError("producer inventory and catalog differ")
    return rows


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--summary", action="store_true")
    args = parser.parse_args()
    rows = inventory()
    if args.summary:
        print(json.dumps({"identifiers": len(rows), "tenant_native_values": sum(r["tenant_native"] for r in rows),
                          "tenant_status_only": sum(r["tenant_disposition"] for r in rows), "source_kind": Counter(r["source_kind"] for r in rows),
                          "publication_path": Counter(r["publication_path"] for r in rows)}, indent=2, sort_keys=True))
    else:
        writer = csv.DictWriter(sys.stdout, fieldnames=rows[0].keys(), delimiter="\t", lineterminator="\n")
        writer.writeheader()
        writer.writerows(rows)


if __name__ == "__main__":
    main()
