#!/usr/bin/env python3
"""Compare the opt-in dbt HRV component with the real Go formula on synthetic snapshots."""
from __future__ import annotations

import argparse
import json
import math
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time

from readiness_hrv_fixture import BASE, CASES

ROOT = Path(__file__).resolve().parents[3]
PILOT = Path(__file__).resolve().parents[1]
IMAGE = "postgres:16-alpine"
LABEL = "codex.health-readiness-hrv-shadow"
DB = "readiness_hrv_shadow"
PASSWORD = "synthetic-only"
NUMERIC = ("current_ms", "recent7_mean_ms", "baseline_mean_ms", "baseline_sd_ms",
           "z_today", "z_trend", "blended_z", "component_weight", "weighted_z")
DISCRETE = ("available", "skip_reason", "n_values")


def run(argv: list[str], *, input_text: str | None = None, check: bool = True,
        capture: bool = False, env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(argv, cwd=ROOT, env=env, input=input_text,
                          text=True, capture_output=capture, check=check)


def docker(*argv: str, **kwargs) -> subprocess.CompletedProcess[str]:
    return run(["docker", *argv], **kwargs)


def psql(container: str, sql: str, *, capture: bool = False) -> str:
    result = docker("exec", "-i", container, "psql", "-X", "-v", "ON_ERROR_STOP=1",
                    "-At", "-U", "pilot_admin", "-d", DB, "-f", "-",
                    input_text=sql, capture=True)
    return result.stdout.strip() if capture else ""


def require_owned(container: str, token: str, *, marker: bool) -> None:
    label = docker("inspect", "-f", f"{{{{ index .Config.Labels \"{LABEL}\" }}}}",
                   container, capture=True).stdout.strip()
    if label != token:
        raise RuntimeError("temporary container label changed; refusing operation")
    if marker and psql(container, "SELECT token FROM public.pilot_identity;", capture=True) != token:
        raise RuntimeError("temporary database marker changed; refusing operation")


def literal(value: object) -> str:
    if value is None:
        return "NULL"
    if isinstance(value, bool):
        return "true" if value else "false"
    if isinstance(value, (int, float)):
        if not math.isfinite(float(value)):
            raise ValueError("non-finite synthetic input")
        return str(value)
    return "'" + str(value).replace("'", "''") + "'"


def snapshot_fixture_sql() -> str:
    statements = ["""CREATE TABLE source_data.readiness_hrv_snapshot_inputs (
 snapshot_id text NOT NULL, profile text NOT NULL, target_date text NOT NULL,
 reader text NOT NULL, reader_path text NOT NULL, snapshot_state text NOT NULL,
 observed_date text, raw_ms double precision, cached_ms double precision,
 repair_pending boolean NOT NULL, cache_row_eligible boolean NOT NULL,
 evidence_present boolean NOT NULL
);"""]
    columns = ("snapshot_id", "profile", "target_date", "reader", "reader_path",
               "snapshot_state", "observed_date", "raw_ms", "cached_ms",
               "repair_pending", "cache_row_eligible", "evidence_present")
    for item in CASES:
        rows = zip(item["candidate_dates"], item["candidate_values"], strict=True)
        rows = list(rows) or [(None, None)]
        for day, value in rows:
            warm = item["reader_path"] == "morning_warm"
            is_today = day == item["target_date"]
            vals = (item["snapshot_id"], item["profile"], item["target_date"],
                    item["reader"], item["reader_path"], item["snapshot_state"],
                    day, value if not warm or (is_today and item["raw_today"]) else None,
                    value if warm else None, is_today and item["repair_today"],
                    warm, item["evidence_present"])
            statements.append("INSERT INTO source_data.readiness_hrv_snapshot_inputs (" +
                              ",".join(columns) + ") VALUES (" +
                              ",".join(literal(v) for v in vals) + ");")
    statements.append("GRANT SELECT ON source_data.readiness_hrv_snapshot_inputs TO pilot_dbt;")
    return "\n".join(statements)


def compare(actual: list[dict], expected: list[dict]) -> None:
    indexed = {row["snapshot_id"]: row for row in actual}
    fixtures = {item["snapshot_id"]: item for item in CASES}
    if len(indexed) != len(actual) or set(indexed) != {row["snapshot_id"] for row in expected}:
        raise AssertionError("dbt snapshot keys differ from Go oracle")
    for oracle in expected:
        name = oracle["snapshot_id"]
        row = indexed[name]
        fixture = fixtures[name]
        for field in ("profile", "target_date", "reader", "reader_path", "snapshot_state", "evidence_present"):
            if row[field] != fixture[field]:
                raise AssertionError(f"{name}.{field}: dbt={row[field]!r}, fixture={fixture[field]!r}")
        if row["input_ordinals"] != list(range(len(row["input_values"]))):
            raise AssertionError(f"{name}: dbt ordinals are not contiguous")
        for field in DISCRETE:
            if row[field] != oracle[field]:
                raise AssertionError(f"{name}.{field}: dbt={row[field]!r}, Go={oracle[field]!r}")
        for actual_field, oracle_field in (("input_dates", "selected_dates"),
                                            ("input_values", "selected_values")):
            if row[actual_field] != oracle[oracle_field]:
                raise AssertionError(f"{name}.{actual_field}: dbt input differs from Go selection")
        for field in NUMERIC:
            got, want = row[field], oracle[field]
            if got is None or want is None:
                if got != want:
                    raise AssertionError(f"{name}.{field}: dbt={got!r}, Go={want!r}")
            elif not (math.isfinite(float(got)) and
                      math.isclose(float(got), float(want), rel_tol=1e-9, abs_tol=1e-9)):
                raise AssertionError(f"{name}.{field}: dbt={got!r}, Go={want!r}")
    print(f"PASS: {len(expected)} synthetic snapshots; exact discrete/input selection; numeric abs/rel tolerance 1e-9")


def check_snapshot_scope(container: str) -> None:
    # Reuse an existing capture ID in a second profile. Its one valid day
    # must not borrow baseline observations from the first profile.
    result = psql(container, """
        BEGIN;
        INSERT INTO source_data.readiness_hrv_snapshot_inputs
            (snapshot_id, profile, target_date, reader, reader_path, snapshot_state,
             observed_date, raw_ms, repair_pending, cache_row_eligible, evidence_present)
        SELECT snapshot_id, 'second-profile', target_date, reader, reader_path, snapshot_state,
               observed_date, 33, false, false, true
        FROM source_data.readiness_hrv_snapshot_inputs
        WHERE snapshot_id='nine_available' AND observed_date=target_date;
        SELECT jsonb_object_agg(profile, jsonb_build_array(n_values,available))
        FROM dbt_output.semantic_readiness_hrv_component_v1 WHERE snapshot_id='nine_available';
        ROLLBACK;
        """, capture=True)
    rows = json.loads(result.splitlines()[-2])
    if rows != {"synthetic": [9, True], "second-profile": [1, False]}:
        raise AssertionError("profiles sharing a capture ID must retain separate observations")


def assert_scenarios(oracle: list[dict]) -> None:
    by_id = {row["snapshot_id"]: row for row in oracle}
    required = {case["snapshot_id"] for case in CASES}
    if set(by_id) != required:
        raise AssertionError("oracle case set incomplete")
    if by_id["eight_insufficient"]["available"] or not by_id["nine_available"]["available"]:
        raise AssertionError("8/9 boundary is not exercised")
    if by_id["constant_baseline"]["baseline_sd_ms"] != 0:
        raise AssertionError("zero-variance baseline is not exercised")
    if by_id["missing_target_day"]["available"]:
        raise AssertionError("missing target-day gate is not exercised")
    if len(by_id["interior_gap"]["selected_dates"]) != 12 or by_id["interior_gap"]["selected_dates"][5] == "2026-11-17":
        raise AssertionError("interior calendar gap is not exercised")
    if by_id["after_late_correction"]["weighted_z"] == by_id["before_late_correction"]["weighted_z"]:
        raise AssertionError("late correction does not change contribution")
    if by_id["after_last_point_deletion"]["available"]:
        raise AssertionError("last-point deletion does not remove contribution")
    if by_id["high_direction"]["weighted_z"] <= by_id["low_direction"]["weighted_z"]:
        raise AssertionError("HRV direction is not exercised")
    if by_id["multi_component"]["score"] == by_id["before_late_correction"]["score"]:
        raise AssertionError("other components do not affect the final score")
    if by_id["nine_available"]["weighted_z"] == 0:
        raise AssertionError("nonzero weighted contribution is not exercised")
    if by_id["morning_known_at_decision"]["selected_values"][0] == by_id["after_late_correction"]["selected_values"][0]:
        raise AssertionError("known-at-decision morning value was overwritten by late correction")
    if by_id["morning_cached_anchor"]["selected_values"][0] != BASE[0]:
        raise AssertionError("same-day cache fallback is not exercised")
    if by_id["morning_unrecoverable"]["score"] is not None:
        raise AssertionError("unrecoverable morning snapshot must not claim a score")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--with-score", action="store_true", help="also compare complete Readiness scores and display caps")
    parser.add_argument("--with-chips", action="store_true", help="also compare four targets, calibration and chip states")
    args = parser.parse_args()
    dbt = PILOT / ".venv/bin/dbt"
    if not dbt.is_file():
        raise RuntimeError("dbt environment missing: install experiments/dbt_metrics/requirements.lock")
    token = "hp-readiness-" + secrets.token_hex(8)
    container = ""
    marker = False
    failure: BaseException | None = None
    with tempfile.TemporaryDirectory(prefix="health-readiness-hrv-") as temp:
        lab = Path(temp)
        profile = lab / "profiles"
        profile.mkdir()
        (profile / "profiles.yml").write_text("""health_dbt_metrics_pilot:
  target: local
  outputs:
    local:
      type: postgres
      host: 127.0.0.1
      port: "{{ env_var('DBT_PILOT_PORT') | int }}"
      user: pilot_dbt
      password: synthetic-only
      dbname: readiness_hrv_shadow
      schema: dbt_output
      threads: 1
""")
        try:
            container = docker("run", "-d", "--name", token, "--label", f"{LABEL}={token}",
                               "-e", "POSTGRES_USER=pilot_admin", "-e", f"POSTGRES_PASSWORD={PASSWORD}",
                               "-e", f"POSTGRES_DB={DB}", "-p", "127.0.0.1::5432", IMAGE,
                               capture=True).stdout.strip()
            require_owned(container, token, marker=False)
            published = docker("port", container, "5432/tcp", capture=True).stdout.strip()
            if not published.startswith("127.0.0.1:"):
                raise RuntimeError("temporary PostgreSQL port is not loopback-bound")
            port = int(published.rsplit(":", 1)[1])
            for _ in range(60):
                if docker("exec", container, "pg_isready", "-h", "127.0.0.1", "-U", "pilot_admin", "-d", DB,
                          check=False, capture=True).returncode == 0:
                    break
                time.sleep(1)
            else:
                raise RuntimeError("temporary PostgreSQL did not become ready")
            psql(container, f"CREATE TABLE public.pilot_identity(token text NOT NULL); "
                            f"INSERT INTO public.pilot_identity VALUES ({literal(token)});")
            marker = True
            require_owned(container, token, marker=True)
            psql(container, (PILOT / "fixtures/schema.sql").read_text())
            psql(container, snapshot_fixture_sql())
            selectors = ["semantic_readiness_hrv_component_v1"]
            variables = {"readiness_hrv_shadow": True}
            if args.with_score:
                import readiness_score_cases as score
                score_samples = score.score_cases()
                score.prepare(psql, container, score_samples)
                selectors.append("semantic_readiness_score_v1")
                variables["readiness_score_shadow"] = True
            if args.with_chips:
                psql(container, (PILOT / "fixtures/readiness_chips.sql").read_text())
                selectors.extend(["chip_targets_v1", "chip_recovery_sleep_v1", "chip_feature_windows_v1", "chip_baseline_v1", "chip_calibration_v1", "chip_display_v1"])
                variables["readiness_chips_shadow"] = True
            denied = docker("exec", container, "psql", "-X", "-U", "pilot_dbt", "-d", DB,
                            "-c", "INSERT INTO source_data.readiness_hrv_snapshot_inputs "
                                  "VALUES ('denied','x','2026-11-22','history','history','captured',"
                                  "'2026-11-22',1,NULL,false,false,true)",
                            check=False, capture=True)
            if denied.returncode == 0 or "permission denied" not in (denied.stdout + denied.stderr).lower():
                raise RuntimeError("dbt role unexpectedly wrote to synthetic source")
            oracle_input = lab / "oracle-input.json"
            oracle_output = lab / "oracle-output.json"
            oracle_input.write_text(json.dumps(CASES, separators=(",", ":")))
            env = {k: v for k, v in os.environ.items() if not k.startswith("PG") and k != "DATABASE_URL"}
            env.update({"READINESS_HRV_ORACLE_INPUT": str(oracle_input),
                        "READINESS_HRV_ORACLE_OUTPUT": str(oracle_output),
                        "DBT_PILOT_PORT": str(port), "DBT_SEND_ANONYMOUS_USAGE_STATS": "false",
                        "GOCACHE": str(lab / "go-cache")})
            run(["go", "test", "./internal/health", "-run", "^TestReadinessHRVOracle$",
                 "-count=1", "-timeout=3m"], env=env)
            oracle = json.loads(oracle_output.read_text())
            assert_scenarios(oracle)
            require_owned(container, token, marker=True)
            run([str(dbt), "build", "--project-dir", str(PILOT), "--profiles-dir", str(profile),
                 "--vars", json.dumps(variables),
                 "--select", *selectors,
                 "--target-path", str(lab / "target"), "--log-path", str(lab / "logs"),
                 "--no-partial-parse"], env=env)
            require_owned(container, token, marker=True)
            dsn = f"postgres://pilot_admin:{PASSWORD}@127.0.0.1:{port}/{DB}?sslmode=disable"
            reader_env = env | {"READINESS_HRV_SHADOW_COMPARE": "1", "HEALTH_DB_TESTS": "1",
                                "READINESS_TEST_DSN": dsn}
            reader_env["READINESS_SCORE_SHADOW_COMPARE"] = "1" if args.with_score else ""
            reader_tests = "^Test(ReadinessHRVReaderInputs|ReadinessScoreReaders)$" if args.with_score else "^TestReadinessHRVReaderInputs$"
            run(["go", "test", "./internal/storage", "-run", reader_tests,
                 "-count=1", "-timeout=3m"], env=reader_env)
            require_owned(container, token, marker=True)
            if args.with_chips:
                reader_env["READINESS_CHIP_SHADOW_COMPARE"] = "1"
                run(["go", "test", "./internal/storage", "./internal/ui", "-run", "^TestChipDBT", "-count=1", "-timeout=3m", "-v"], env=reader_env)
            raw = psql(container, "SELECT COALESCE(json_agg(t ORDER BY snapshot_id), '[]'::json) "
                                  "FROM (SELECT * FROM dbt_output.semantic_readiness_hrv_component_v1) t;",
                       capture=True)
            compare(json.loads(raw), oracle)
            check_snapshot_scope(container)
            if args.with_score:
                score.verify(run, psql, container, score_samples, lab, env)
                if env.get("HEALTH_METRIC_AUDIT_DIR"):
                    run([str(PILOT/".venv/bin/python"), str(PILOT/"scripts/capture_bound_metrics.py"), str(port), "readiness",
                         *([] if args.with_chips else ["semantic_readiness_score_v1"])], env=env)
        except BaseException as exc:
            failure = exc
            raise
        finally:
            if container:
                try:
                    require_owned(container, token, marker=marker)
                    volumes = docker("inspect", "-f", "{{range .Mounts}}{{if .Name}}{{println .Name}}{{end}}{{end}}",
                                     container, capture=True).stdout.split()
                    docker("rm", "-f", "-v", container, capture=True)
                    if docker("inspect", container, check=False, capture=True).returncode == 0:
                        raise RuntimeError("owned container remains after cleanup")
                    for volume in volumes:
                        if docker("volume", "inspect", volume, check=False, capture=True).returncode == 0:
                            raise RuntimeError(f"owned anonymous volume remains: {volume}")
                except Exception as cleanup_error:
                    if failure is not None:
                        failure.add_note(f"cleanup also failed: {cleanup_error}")
                    else:
                        raise
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
