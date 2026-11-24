"""Emit dbt variables for the tenant's native catalog read view."""
import argparse
import json
import re

from profile_graph_catalog import graph_catalog_vars


def native_catalog_vars(schema):
    if not re.fullmatch("[a-z][a-z0-9_]{0,62}", schema) or schema.startswith("pg_") or schema in {
        "public", "information_schema", "health_registry"
    }:
        raise ValueError("invalid tenant schema")
    result = graph_catalog_vars()
    supported_models = {
        "briefing_metric_trends_v1",
        "briefing_section_status_v1",
        "briefing_signals_v1",
        "captured_briefing_fields_v1",
        "captured_dashboard_sleep_quality_v1",
        "captured_illness_evidence_v1",
        "captured_readiness_component_v1",
        "chip_baseline_v1",
        "chip_calibration_v1",
        "chip_targets_v1",
        "hrv_fixed_window_baseline_v1",
        "oxygen_cluster_v1",
        "personal_baseline_v1",
        "semantic_energy_guidance_v1",
        "semantic_energy_legacy_v1",
        "semantic_hrv_after_workout_v1",
        "semantic_hrv_daily_v1",
        "semantic_hrv_night_v1",
        "semantic_hrv_observation_v1",
        "semantic_hrv_workout_v1",
        "semantic_sleep_continuity_v1",
        "semantic_sleep_day_v1",
        "semantic_sleep_duration_comparison_v1",
        "semantic_sleep_history_v1",
        "semantic_sleep_quality_v1",
        "shadow_sleep_daily_v1",
        "sleep_architecture_days_v1",
        "sleep_architecture_windows_v1",
        "sleep_balance_history_v1",
        "sleep_duration_balance_v1",
        "sleep_duration_reference_v1",
        "sleep_regularity_index_v1",
        "stress_hour_z_v1",
        "weekly_sleep_analysis_v1",
        "workout_period_v1",
        "workout_type_v1",
    }
    decisions = {"semantic_energy_bank_v3", "semantic_readiness_score_v1", "daily_decision_boundary_v1"}
    groups = []
    for group in result["profile_graph_models"]:
        model = group["model"]
        if model != "daily_metrics" and not model.startswith("archived_") and model not in supported_models | decisions:
            raise ValueError(f"missing tenant-native source for catalog model {model}")
        group["published_decision"] = model in decisions
        groups.append(group)
    archive_features = next(group for group in groups if group["model"] == "archived_features_v1")
    for model in ("recorded_feature_decision_v1", "native_feature_replay_v1"):
        groups.append(dict(archive_features, model=model, keys=[*archive_features["keys"], "revision"]))
    wake = next(group for group in groups if group["model"] == "archived_wake_time_v1")
    groups.append(dict(wake, model="wake_confirmation_v1", keys=["profile", "capture"],
                       fields=[dict(wake["fields"][0], column="candidate_wake")],
                       metadata={"observed_at": "candidate_wake", "available_at": "decision_at", "version": "inputs_hash"}))
    result["profile_graph_models"] = groups
    result["native_catalog_bindings"] = sorted({field["metric"] for group in groups for field in group["fields"]})
    result.update(tenant_native_catalog=True, profile_metric_reads=True,
                  archived_profile_fields=True, semantic_source_schema=schema, archive_records_schema=schema)
    return result


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("schema")
    print(json.dumps(native_catalog_vars(parser.parse_args().schema)))
