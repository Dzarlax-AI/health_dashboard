#!/usr/bin/env python3
"""Run the isolated synthetic dbt pilot in a newly created local container."""
from __future__ import annotations

import argparse
import fcntl
import json
import math
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time
import uuid

from check_semantic_catalog import hourly_sum_metrics

ROOT = Path(__file__).resolve().parents[3]
HERE = Path(__file__).resolve().parents[1]
TOKEN = "hp-dbt-pilot-" + uuid.uuid4().hex[:16]
NAME = TOKEN
PASSWORD = "synthetic-only"
DB = "hp_dbt_pilot"
IMAGE = "postgres@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24"


def run(args: list[str], *, input_text: str | None = None, check: bool = True,
        capture: bool = False, env: dict[str, str] | None = None,
        cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, input=input_text, text=True, check=check,
                          capture_output=capture, env=env, cwd=cwd)


def docker(*args: str, **kwargs) -> subprocess.CompletedProcess[str]:
    return run(["docker", *args], **kwargs)


def ensure_runtime() -> Path:
    if sys.version_info < (3, 12):
        raise RuntimeError("the pilot runtime requires Python 3.12 or newer")
    venv = HERE / ".venv"
    lock = HERE / "requirements.lock"
    python = venv / "bin" / "python"
    dbt = venv / "bin" / "dbt"
    if not dbt.exists():
        run([sys.executable, "-m", "venv", str(venv)])
        run([str(python), "-m", "pip", "install", "--disable-pip-version-check",
             "-r", str(lock if lock.exists() else HERE / "requirements.txt")])
    if not lock.exists():
        frozen = run([str(python), "-m", "pip", "freeze", "--all"], capture=True)
        lock.write_text(frozen.stdout)
    return dbt


def load_sql(container_id: str, sql: str) -> None:
    docker("exec", "-i", container_id, "psql", "-v", "ON_ERROR_STOP=1",
           "-U", "pilot_admin", "-d", DB, "-f", "-", input_text=sql)


def scalar(container_id: str, sql: str) -> str:
    return docker("exec", container_id, "psql", "-At", "-U", "pilot_admin",
                  "-d", DB, "-c", sql, capture=True).stdout.strip()


def require_owned(container_id: str, token: str) -> None:
    labels = docker("inspect", "-f", "{{ index .Config.Labels \"codex.health-dbt-pilot\" }}",
                    container_id, capture=True).stdout.strip()
    marker = scalar(container_id, "SELECT token FROM public.pilot_identity")
    if labels != token or marker != token:
        raise RuntimeError("pilot identity changed; refusing database operation")


def compare(actual: dict, expected: dict) -> None:
    for key, value in expected.items():
        if key not in actual:
            raise RuntimeError(f"missing expected key: {key}")
        observed = actual[key]
        if isinstance(value, dict):
            compare(observed, value)
        elif isinstance(value, list):
            if len(observed) != len(value):
                raise RuntimeError(f"{key}: row count {len(observed)} != {len(value)}")
            for i, (left, right) in enumerate(zip(observed, value)):
                compare(left, right)
        elif isinstance(value, (float, int)) and value is not None:
            if observed is None or not math.isfinite(float(observed)) or abs(float(observed) - value) > 1e-9 + 1e-9 * abs(value):
                raise RuntimeError(f"{key}: {observed!r} != {value!r}")
        elif observed != value:
            raise RuntimeError(f"{key}: {observed!r} != {value!r}")


def read_model_rows(container_id: str, model: str, order: str) -> list[dict]:
    output = scalar(container_id, f"SELECT COALESCE(json_agg(t ORDER BY {order}), '[]'::json)::text FROM (SELECT * FROM dbt_output.{model}) t")
    return json.loads(output)


def read_outputs(container_id: str) -> dict[str, list[dict]]:
    return {"hourly": read_model_rows(container_id, "hourly_metrics", "metric_name,hour,source"),
            "daily": read_model_rows(container_id, "daily_metrics", "date")}


def run_go(dsn: str, token: str, dates: list[str], output: Path, reset: bool) -> dict:
    child_env = os.environ.copy()
    child_env.pop("DATABASE_URL", None)
    with tempfile.NamedTemporaryFile(dir=HERE / "artifacts", prefix=".oracle-", delete=False) as scratch:
        oracle_path = Path(scratch.name)
    try:
        child_env.update({"DBT_PILOT_ENABLE": "synthetic-only", "DBT_PILOT_DSN": dsn,
                          "DBT_PILOT_TOKEN": token, "DBT_PILOT_DATES": json.dumps(dates),
                          "DBT_PILOT_RESET": "1" if reset else "0", "DBT_PILOT_OUTPUT": scratch.name})
        run(["go", "test", "./internal/storage", "-run", "^TestDBTMetricsPilot$", "-count=1"],
            env=child_env, cwd=ROOT)
        result = json.loads(oracle_path.read_text())
        oracle_path.replace(output)
        return result
    finally:
        oracle_path.unlink(missing_ok=True)


def keyed(rows: list[dict], key_fields: tuple[str, ...]) -> dict[tuple, dict]:
    result = {tuple(row[field] for field in key_fields): row for row in rows}
    if len(result) != len(rows):
        raise RuntimeError(f"duplicate keys detected for {key_fields}")
    return result


def assert_same_outputs(actual: dict, expected: dict, context: str) -> None:
    for group, key_fields in (("hourly", ("metric_name", "hour", "source")), ("daily", ("date",))):
        left, right = keyed(actual[group], key_fields), keyed(expected[group], key_fields)
        if left.keys() != right.keys():
            raise RuntimeError(f"{context}: {group} keys differ ({len(left)} vs {len(right)})")
        for key in left:
            compare(left[key], right[key])


def assert_untouched(before: dict, after: dict, touched: set[str], context: str) -> None:
    for group, date_of in (("hourly", lambda row: row["hour"][:10]), ("daily", lambda row: row["date"])):
        left = {key: row for key, row in keyed(before[group], ("metric_name", "hour", "source") if group == "hourly" else ("date",)).items()
                if date_of(row) not in touched}
        right = {key: row for key, row in keyed(after[group], ("metric_name", "hour", "source") if group == "hourly" else ("date",)).items()
                 if date_of(row) not in touched}
        if left.keys() != right.keys():
            raise RuntimeError(f"{context}: untouched {group} keys changed")
        for key in left:
            compare(right[key], left[key])


def dbt_incremental(dbt: Path, dbt_env: dict[str, str], dates: list[str], mode: str = "snapshot") -> None:
    vars_json = json.dumps({"affected_dates": dates, "verified_step_withdrawals": True, "aggregate_capture_mode": mode}, separators=(",", ":"))
    run([str(dbt), "run", "--select", "+daily_metrics", "--vars", vars_json,
         "--project-dir", str(HERE), "--profiles-dir", dbt_env["DBT_PROFILES_DIR"]], env=dbt_env, cwd=ROOT)
    run([str(dbt), "test", "--project-dir", str(HERE), "--profiles-dir", dbt_env["DBT_PROFILES_DIR"]], env=dbt_env, cwd=ROOT)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--capture-bindings", action="store_true",help="export typed columns after the initial Go/dbt/golden comparison")
    args = parser.parse_args()
    if args.capture_bindings and not os.getenv("HEALTH_METRIC_AUDIT_DIR"):
        parser.error("--capture-bindings requires HEALTH_METRIC_AUDIT_DIR")
    artifacts = HERE / "artifacts"
    artifacts.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix=".pilot-", dir=artifacts) as scratch:
        return run_pilot(Path(scratch), args.capture_bindings)


def run_pilot(lab: Path, capture_bindings: bool = False) -> int:
    with (HERE / "artifacts/.pilot-runtime.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        dbt = ensure_runtime()
    # bind an ephemeral host port to loopback only; container is disposable
    docker("run", "-d", "--name", NAME, "--label", f"codex.health-dbt-pilot={TOKEN}",
           "-e", f"POSTGRES_USER=pilot_admin", "-e", f"POSTGRES_PASSWORD={PASSWORD}",
           "-e", f"POSTGRES_DB={DB}", "-p", "127.0.0.1::5432", IMAGE)
    container_id = docker("inspect", "-f", "{{.Id}}", NAME, capture=True).stdout.strip()
    try:
        labels = docker("inspect", "-f", "{{ index .Config.Labels \"codex.health-dbt-pilot\" }}", container_id, capture=True).stdout.strip()
        if labels != TOKEN or not container_id:
            raise RuntimeError("new container marker mismatch; refusing to use it")
        port_text = docker("port", container_id, "5432/tcp", capture=True).stdout.strip()
        if not port_text.startswith("127.0.0.1:"):
            raise RuntimeError("container port is not loopback-bound")
        port = int(port_text.rsplit(":", 1)[1])
        for _ in range(60):
            ready = docker("exec", container_id, "pg_isready", "-U", "pilot_admin", "-d", DB, check=False, capture=True)
            if ready.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError("temporary PostgreSQL did not become ready")

        load_sql(container_id, f"CREATE TABLE public.pilot_identity(token text NOT NULL); INSERT INTO public.pilot_identity VALUES ('{TOKEN}');\n")
        require_owned(container_id, TOKEN)
        load_sql(container_id, (HERE / "fixtures/schema.sql").read_text())
        require_owned(container_id, TOKEN)
        load_sql(container_id, (HERE / "fixtures/base.sql").read_text())
        dates = [f"2026-01-{i:02d}" for i in range(1, 16)]
        dsn = f"postgres://pilot_admin:{PASSWORD}@127.0.0.1:{port}/{DB}?sslmode=disable"
        output_path = lab / "oracle.json"
        env = os.environ.copy()
        env.pop("DATABASE_URL", None)
        go_started = time.perf_counter()
        oracle = run_go(dsn, TOKEN, dates, output_path, reset=True)
        go_seconds = time.perf_counter() - go_started

        # A harmless read probe confirms dbt role access; mutation must be denied.
        read_probe = docker("exec", container_id, "psql", "-At", "-U", "pilot_dbt", "-d", DB,
                            "-c", "SELECT COUNT(*) FROM source_data.metric_points", capture=True).stdout.strip()
        denied = docker("exec", container_id, "psql", "-U", "pilot_dbt", "-d", DB,
                        "-c", "INSERT INTO source_data.metric_points VALUES ('x','2026-01-01 00:00:00','x',1,'ok')",
                        check=False, capture=True)
        if denied.returncode == 0 or "permission denied" not in (denied.stdout + denied.stderr).lower():
            raise RuntimeError("dbt role unexpectedly wrote to source_data")

        profiles = lab / "profiles.yml"
        shutil.copyfile(HERE / "profiles.yml.example", profiles)
        dbt_env = env | {"DBT_PILOT_PORT": str(port), "DBT_PROFILES_DIR": str(lab),
                         "DBT_TARGET_PATH": str(lab / "target"), "DBT_LOG_PATH": str(lab / "logs"),
                         "DBT_SEND_ANONYMOUS_USAGE_STATS": "false"}
        require_owned(container_id, TOKEN)
        dbt_started = time.perf_counter()
        run([str(dbt), "build", "--project-dir", str(HERE), "--profiles-dir", dbt_env["DBT_PROFILES_DIR"]], env=dbt_env, cwd=ROOT)
        dbt_seconds = time.perf_counter() - dbt_started
        hourly = read_model_rows(container_id, "hourly_metrics", "metric_name,hour,source")
        daily = read_model_rows(container_id, "daily_metrics", "date")
        actual = {"hourly": hourly, "daily": daily}
        compare(actual, {"hourly": oracle["hourly"], "daily": oracle["daily"]})

        expected = json.loads((HERE / "fixtures/expected.json").read_text())
        for label, result in (("Go", oracle), ("dbt", actual)):
            # Expected-file assertions below are done against keyed row maps.
            hmap = {(r["metric_name"], r["hour"], r["source"]): r for r in result["hourly"]}
            dmap = {r["date"]: r for r in result["daily"]}
            for partial in expected["hourly"]:
                key = (partial["metric_name"], partial["hour"], partial["source"])
                compare(hmap[key], partial)
            for partial in expected["absent_hourly"]:
                key = (partial["metric_name"], partial["hour"], partial["source"])
                if key in hmap:
                    raise RuntimeError(f"{label}: unexpected hourly row {key}")
            for date, partial in expected["daily"].items():
                compare(dmap[date], partial)
        dbt_sums = sorted(hourly_sum_metrics())
        if dbt_sums != oracle["sum_metrics"]:
            raise RuntimeError("dbt SUM metric list differs from the Go oracle")
        if capture_bindings:
            run([str(dbt.parent / "python"), str(HERE / "scripts/capture_bound_metrics.py"), str(port), "aggregates"], env=env, cwd=ROOT)
            return 0
        rejected_inputs = []
        for label, vars_arg in (("missing_dates", "{}"),
                                ("non_list_dates", '{"affected_dates":"2026-01-01"}'),
                                ("invalid_calendar_date", '{"affected_dates":["2026-99-99"]}'),
                                ("date_missing_from_calendar", '{"affected_dates":["2026-01-16"]}'),
                                ("partial_full_refresh", '{"affected_dates":["2026-01-01"]}')):
            extra_flags = ["--full-refresh"] if label == "partial_full_refresh" else []
            rejected = run([str(dbt), "run", "--select", "hourly_metrics", "--vars", vars_arg,
                            "--project-dir", str(HERE), "--profiles-dir", dbt_env["DBT_PROFILES_DIR"], *extra_flags],
                           env=dbt_env, cwd=ROOT, check=False, capture=True)
            if rejected.returncode == 0:
                raise RuntimeError(f"dbt accepted invalid incremental input: {label}")
            rejected_inputs.append(label)
        assert_same_outputs(read_outputs(container_id), actual, "rejected inputs preserve output")

        phase_results = []

        def phase(label: str, date: str, sql: str, mode: str = "snapshot"):
            nonlocal oracle, actual
            before_go, before_dbt = oracle, actual
            require_owned(container_id, TOKEN)
            load_sql(container_id, sql)
            oracle = run_go(dsn, TOKEN, [date], output_path, reset=False)
            require_owned(container_id, TOKEN)
            dbt_incremental(dbt, dbt_env, [date], mode)
            actual = read_outputs(container_id)
            assert_untouched(before_go, oracle, {date}, label + " Go")
            assert_untouched(before_dbt, actual, {date}, label + " dbt")
            assert_same_outputs(actual, oracle, label)
            phase_results.append({"phase": label, "result": "go_dbt_match"})
            daily = keyed(actual["daily"], ("date",))
            physical_expected = {"step_fold": 110, "step_zero": 0, "step_cache_only": 80,
                                 "step_partial_same_source": 80, "step_restore": 5, "step_zero_revoked": None}
            if label in physical_expected:
                compare(daily[(date,)], {"steps": physical_expected[label]})
            if label in ("disjoint_step_hours", "withdraw_hour_winners"):
                compare(daily[(date,)], {"steps": 34 if label == "disjoint_step_hours" else 123})
            if label in ("prior_daily_without_hour","prior_daily_with_receipt"):
                want = 432 if label == "prior_daily_without_hour" else None
                if daily[(date,)]["steps"] != want:
                    raise RuntimeError(f"{label}: expected {want} steps, got {daily[(date,)]['steps']}")
            if label == "daily_average_source_weights":
                compare(daily[(date,)], {"rhr_avg": 64, "spo2_avg": 94, "vo2_avg": 39, "resp_avg": 16})
            if label in ("two_resting_hr_sources", "partial_resting_hr"):
                compare(daily[(date,)], {"rhr_avg": 60 if label == "two_resting_hr_sources" else 62})
            if label in ("late_step_in_same_hour", "missing_watch_without_receipt", "physical_watch_with_receipt", "watch_returns_after_receipt"):
                want = {"physical_watch_with_receipt": 100, "watch_returns_after_receipt": 35}.get(label, 40)
                if daily[(date,)]["steps"] != want:
                    raise RuntimeError(f"{label}: expected {want} steps, got {daily[(date,)]['steps']}")
            if label == "partial_step_retransmission":
                assert_same_outputs(actual, before_dbt, label)
                if daily[(date,)]["steps"] != 35:
                    raise RuntimeError("partial iPhone retransmission changed preferred Watch steps")
            if label == "quality_invalidation" and daily[(date,)]["steps"] != 100:
                raise RuntimeError("explicitly withdrawn Watch steps must fall back to iPhone")
            if label == "all_steps_withdrawn" and daily[(date,)]["steps"] is not None:
                raise RuntimeError("withdrawing the final step source must yield NULL")
            if label == "staged_to_coarse_sleep":
                row = daily[("2026-01-03",)]
                if row["sleep_total"] != 4.9 or row["sleep_unspecified"] != 4.9 or any(row[name] is not None for name in ("sleep_deep","sleep_rem","sleep_core","sleep_awake")):
                    raise RuntimeError("staged-to-coarse refresh did not atomically clear stage columns")
            if label == "incomplete_after_complete_sleep":
                before_day = keyed(before_dbt["daily"], ("date",))[(date,)]
                after_day = daily[(date,)]
                for name in ("sleep_total","sleep_deep","sleep_rem","sleep_core","sleep_awake","sleep_unspecified"):
                    compare(after_day, {name: before_day[name]})

        # An identical historical-date rebuild must be stable and idempotent.
        phase("repeat_no_change", "2026-01-01", "SELECT 1;\n")
        phase("late_old_date_point", "2026-01-02", """
INSERT INTO source_data.metric_points VALUES
 ('heart_rate_variability','2026-01-02 08:30:00 +0100','Other',33.25,'ok');
""")
        phase("updated_existing_point", "2026-01-01", """
UPDATE source_data.metric_points SET qty=26
WHERE metric_name='heart_rate_variability' AND date='2026-01-01 09:00:00 +0200' AND source='Apple Watch';
""")
        phase("staged_to_coarse_sleep", "2026-01-03", """
UPDATE source_data.metric_points SET qty=4.9
WHERE SUBSTRING(date,1,10)='2026-01-03' AND source='RingConn'
  AND metric_name IN ('sleep_total','sleep_unspecified');
""")
        phase("stage_complete_sleep", "2026-01-15", """
INSERT INTO source_data.metric_points VALUES
 ('sleep_total','2026-01-15 00:00:00 +0100','Apple Watch',7,'ok'),
 ('sleep_deep','2026-01-15 00:00:00 +0100','Apple Watch',1,'ok'),
 ('sleep_rem','2026-01-15 00:00:00 +0100','Apple Watch',2,'ok'),
 ('sleep_core','2026-01-15 00:00:00 +0100','Apple Watch',3.5,'ok'),
 ('sleep_awake','2026-01-15 00:00:00 +0100','Apple Watch',0.5,'ok');
""")
        phase("incomplete_after_complete_sleep", "2026-01-15", """
INSERT INTO source_data.metric_points VALUES
 ('sleep_total','2026-01-15 00:00:00 +0100','Other',4.9,'ok');
""")
        phase("daily_average_source_weights", "2026-01-15",
              (HERE / "fixtures/daily_average_sources.sql").read_text())
        phase("partial_step_retransmission", "2026-01-01", """
INSERT INTO source_data.metric_points VALUES
 ('step_count','2026-01-01 10:00:00 +0200','iPhone',100,'ok')
ON CONFLICT (metric_name,date,source) DO UPDATE SET qty=EXCLUDED.qty, quality=EXCLUDED.quality;
""")
        phase("late_step_in_same_hour", "2026-01-01", """
TRUNCATE source_data.metric_point_changes;
INSERT INTO source_data.metric_point_changes VALUES ('step_count','2026-01-01 10:03:00 +0200','Apple Watch',5,'ok');
INSERT INTO source_data.metric_points SELECT * FROM source_data.metric_point_changes;
""", mode="partial")
        # The same empty change batch first lacks an edit receipt, then gets
        # an authoritative one. Only the second pass may revoke the Watch hour.
        phase("missing_watch_without_receipt", "2026-01-01", """
DROP TABLE dbt_output.step_hour_state;
TRUNCATE source_data.metric_point_changes;
""", mode="partial")
        phase("physical_watch_with_receipt", "2026-01-01", """
INSERT INTO source_data.verified_step_withdrawals
SELECT date,source FROM source_data.metric_points WHERE metric_name='step_count' AND source='Apple Watch'
AND substring(date,1,13)='2026-01-01 10';
DELETE FROM source_data.metric_points p USING source_data.verified_step_withdrawals w
WHERE p.metric_name='step_count' AND (p.date,p.source)=(w.point_date,w.source);
""", mode="partial")
        phase("watch_returns_after_receipt", "2026-01-01", """
DELETE FROM source_data.verified_step_withdrawals;
INSERT INTO source_data.metric_point_changes VALUES ('step_count','2026-01-01 10:00:00 +0200','Apple Watch',35,'ok');
INSERT INTO source_data.metric_points SELECT * FROM source_data.metric_point_changes;
""", mode="partial")
        phase("disjoint_step_hours", "2026-01-14", (HERE / "fixtures/hourly_step_priority.sql").read_text())
        phase("withdraw_hour_winners", "2026-01-14", """
UPDATE source_data.metric_points SET quality='invalid' WHERE metric_name='step_count'
AND substring(date,1,13)='2026-01-14 08' AND source IN ('Apple Watch','Ultra');
""")
        phase("step_fold", "2026-01-10", """
INSERT INTO source_data.health_records VALUES (501,'application/json; metrics-contract=2');
INSERT INTO source_data.metric_points VALUES
 ('step_count','2026-01-10 02:00:00 +0200','Apple Watch',10,'ok',501),
 ('step_count','2026-01-10 02:00:00 +0100','iPhone',100,'ok',501),
 ('step_count','2026-01-10 02:00:00 +0100','Apple Watch',0,'ok',501);
""")
        phase("step_zero", "2026-01-11", """
INSERT INTO source_data.metric_points VALUES ('step_count','2026-01-11 13:00:00 +0100','iPhone',0,'ok',501);
""")
        phase("step_zero_revoked", "2026-01-11", """
UPDATE source_data.metric_points SET quality='invalid' WHERE metric_name='step_count' AND substring(date,1,10)='2026-01-11';
""")
        phase("step_cache_only", "2026-01-12", """
INSERT INTO go_oracle.hourly_metrics VALUES ('step_count','2026-01-12 13:00','iPhone',80,80,80,1);
INSERT INTO dbt_output.step_hour_state VALUES ('2026-01-12 13:00','iPhone',80,80,80,1,false);
INSERT INTO source_data.metric_points VALUES ('step_count','2026-01-12 13:01:00 +0100','Apple Watch',5,'ok',501);
""")
        phase("step_partial_same_source", "2026-01-12", """
INSERT INTO source_data.metric_points VALUES ('step_count','2026-01-12 13:01:00 +0100','iPhone',5,'ok',501);
""")
        phase("step_restore", "2026-01-12", """
INSERT INTO source_data.metric_points VALUES ('step_count','2026-01-12 13:02:00 +0100','iPhone',80,'ok',501);
""")
        phase("two_resting_hr_sources", "2026-01-13", """
INSERT INTO source_data.metric_points VALUES
('resting_heart_rate','2026-01-13 07:00:00 +0100','A',50,'ok'),
('resting_heart_rate','2026-01-13 07:00:00 +0100','B',70,'ok');
""")
        phase("partial_resting_hr", "2026-01-13", """
TRUNCATE source_data.metric_point_changes;
INSERT INTO source_data.metric_point_changes VALUES ('resting_heart_rate','2026-01-13 07:00:00 +0100','B',74,'ok');
UPDATE source_data.metric_points SET qty=74 WHERE metric_name='resting_heart_rate'
AND date='2026-01-13 07:00:00 +0100' AND source='B';
""", mode="partial")
        phase("quality_invalidation", "2026-01-01", """
UPDATE source_data.metric_points SET quality='invalid'
WHERE metric_name='step_count' AND SUBSTRING(date,1,10)='2026-01-01' AND source='Apple Watch';
""")

        phase("all_steps_withdrawn", "2026-01-01", """
UPDATE source_data.metric_points SET quality='invalid' WHERE metric_name='step_count'
AND substring(date,1,10)='2026-01-01' AND source in ('iPhone','Other');
""")
        phase("step_source_returns", "2026-01-01", """
UPDATE source_data.metric_points SET quality='ok' WHERE metric_name='step_count'
AND substring(date,1,10)='2026-01-01' AND source in ('iPhone','Other');
""")

        # A historic summary may predate all hourly rows. An unrelated invalid
        # point alone proves no previous cell changed. A verified edit does.
        phase("prior_daily_without_hour", "2026-01-13", """
UPDATE go_oracle.daily_scores SET steps=432 WHERE date='2026-01-13';
UPDATE dbt_output.daily_metrics SET steps=432 WHERE date='2026-01-13';
INSERT INTO source_data.metric_points VALUES ('step_count','2026-01-13 09:00:00 +0200','Other',0,'invalid');
""")
        phase("prior_daily_with_receipt", "2026-01-13", """
INSERT INTO source_data.verified_step_withdrawals VALUES ('2026-01-13 08:00:00 +0100','Watch');
""")
        load_sql(container_id, "DELETE FROM source_data.verified_step_withdrawals;")

        incremental_sleep = keyed(actual["daily"], ("date",))[("2026-01-15",)]["sleep_total"]
        if incremental_sleep != 7:
            raise RuntimeError("incomplete update did not preserve prior complete sleep")

        require_owned(container_id, TOKEN)
        load_sql(container_id, """
INSERT INTO source_data.calendar(date)
SELECT to_char(d,'YYYY-MM-DD') FROM generate_series('2025-01-01'::date,'2025-12-31'::date,'1 day') d;
INSERT INTO source_data.metric_points(metric_name,date,source,qty,quality)
SELECT m.metric_name,
       to_char(d + h * interval '1 hour','YYYY-MM-DD HH24:MI:SS') || ' +0000',
       'Synthetic Scale',
       CASE m.metric_name WHEN 'heart_rate_variability' THEN 35 + h / 10.0 ELSE 100 + h END,
       'ok'
FROM generate_series('2025-01-01'::date,'2025-12-31'::date,'1 day') d
CROSS JOIN generate_series(0,23) h
CROSS JOIN (VALUES ('heart_rate_variability'),('step_count')) m(metric_name);
""")
        all_dates = [f"2025-{month:02d}-{day:02d}" for month, lengths in
                     ((1,31),(2,28),(3,31),(4,30),(5,31),(6,30),(7,31),(8,31),(9,30),(10,31),(11,30),(12,31))
                     for day in range(1, lengths + 1)] + dates
        go_timings, go_aggregate_timings, dbt_timings = [], [], []
        for trial in range(3):
            go_started = time.perf_counter()
            oracle = run_go(dsn, TOKEN, all_dates, output_path, reset=True)
            go_timings.append(time.perf_counter() - go_started)
            go_aggregate_timings.append(oracle["seconds"])
            require_owned(container_id, TOKEN)
            dbt_started = time.perf_counter()
            result = run([str(dbt), "run", "--full-refresh", "--project-dir", str(HERE), "--profiles-dir", dbt_env["DBT_PROFILES_DIR"]],
                         env=dbt_env, cwd=ROOT, capture=True)
            dbt_timings.append(time.perf_counter() - dbt_started)
            if result.returncode != 0:
                raise RuntimeError("dbt full-refresh benchmark failed:\n" + result.stdout + result.stderr)
        actual = read_outputs(container_id)
        assert_same_outputs(actual, oracle, "benchmark final full reconstruction")
        full_daily = keyed(actual["daily"], ("date",))
        compare(full_daily[("2026-01-14",)], {"steps": 123})
        if full_daily[("2026-01-15",)]["sleep_total"] is not None or full_daily[("2026-01-01",)]["steps"] != 100:
            raise RuntimeError("full reconstruction did not expose expected history-dependent differences")

        old_day_go_wall, old_day_go_aggregate, old_day_dbt_wall = [], [], []
        for trial, qty in enumerate((36, 37, 38), start=1):
            require_owned(container_id, TOKEN)
            load_sql(container_id, f"""
UPDATE source_data.metric_points SET qty={qty}
WHERE metric_name='heart_rate_variability' AND date='2025-01-01 00:00:00 +0000' AND source='Synthetic Scale';
""")
            before_oracle, before_actual = oracle, actual
            started = time.perf_counter()
            oracle = run_go(dsn, TOKEN, ["2025-01-01"], output_path, reset=False)
            old_day_go_wall.append(time.perf_counter() - started)
            old_day_go_aggregate.append(oracle["seconds"])
            vars_json = '{"affected_dates":["2025-01-01"]}'
            started = time.perf_counter()
            result = run([str(dbt), "run", "--select", "+daily_metrics", "--vars", vars_json,
                          "--project-dir", str(HERE), "--profiles-dir", dbt_env["DBT_PROFILES_DIR"]], env=dbt_env, cwd=ROOT, capture=True)
            old_day_dbt_wall.append(time.perf_counter() - started)
            if result.returncode != 0:
                raise RuntimeError(f"dbt old-day trial {trial} failed:\n" + result.stdout + result.stderr)
            actual = read_outputs(container_id)
            assert_same_outputs(actual, oracle, f"old-day trial {trial}")
            assert_untouched(before_oracle, oracle, {"2025-01-01"}, f"old-day trial {trial} Go")
            assert_untouched(before_actual, actual, {"2025-01-01"}, f"old-day trial {trial} dbt")
        test_result = run([str(dbt), "test", "--project-dir", str(HERE), "--profiles-dir", dbt_env["DBT_PROFILES_DIR"]],
                          env=dbt_env, cwd=ROOT, capture=True)
        if test_result.returncode != 0:
            raise RuntimeError("final dbt tests failed:\n" + test_result.stdout + test_result.stderr)

        run([str(dbt), "compile", "--vars", '{"affected_dates":["2025-01-01"]}',
             "--project-dir", str(HERE), "--profiles-dir", dbt_env["DBT_PROFILES_DIR"]], env=dbt_env, cwd=ROOT, capture=True)
        explain_outputs = {}
        compiled_dir = lab / "target/compiled/health_dbt_metrics_pilot/models"
        for model in ("hourly_metrics", "daily_metrics"):
            sql_file = compiled_dir / f"{model}.sql"
            plan = docker("exec", "-i", container_id, "psql", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1",
                          "-U", "pilot_admin", "-d", DB, "-f", "-", input_text="EXPLAIN (ANALYZE, BUFFERS)\n" + sql_file.read_text(),
                          capture=True).stdout
            explain_outputs["one_day_" + model] = plan
        run([str(dbt), "compile", "--full-refresh", "--project-dir", str(HERE), "--profiles-dir", dbt_env["DBT_PROFILES_DIR"]],
            env=dbt_env, cwd=ROOT, capture=True)
        for model in ("hourly_metrics", "daily_metrics"):
            sql_file = compiled_dir / f"{model}.sql"
            plan = docker("exec", "-i", container_id, "psql", "-X", "-A", "-t", "-v", "ON_ERROR_STOP=1",
                          "-U", "pilot_admin", "-d", DB, "-f", "-", input_text="EXPLAIN (ANALYZE, BUFFERS)\n" + sql_file.read_text(),
                          capture=True).stdout
            explain_outputs["full_" + model] = plan
        for name, output in explain_outputs.items():
            (lab / f"explain-{name}.txt").write_text(output)
        # The offline feed retains the pre-image. Stop after committing the
        # raw capture, then retry the whole graph before publishing the edit.
        edit_date = "2026-01-14"
        load_sql(container_id, """
TRUNCATE source_data.metric_point_changes;
INSERT INTO source_data.metric_point_changes VALUES
 ('step_count','2026-01-14 17:00:00 +0100','iPhone',30,'ok'),
 ('step_count','2026-01-14 17:02:00 +0100','iPhone',40,'ok');
""")
        dbt_incremental(dbt, dbt_env, [edit_date], "partial")
        load_sql(container_id, """
TRUNCATE source_data.metric_point_changes;
INSERT INTO source_data.metric_point_changes VALUES
 ('step_count','2026-01-14 17:01:00 +0100','iPhone',99,'ok'),
 ('step_count','2026-01-14 17:01:00 +0100','Apple Watch',7,'ok');
""")
        edit_vars = json.dumps({"affected_dates": [edit_date], "aggregate_capture_mode": "partial"})
        run([str(dbt), "run", "--select", "retained_metric_points", "--vars", edit_vars,
             "--project-dir", str(HERE), "--profiles-dir", dbt_env["DBT_PROFILES_DIR"]], env=dbt_env, cwd=ROOT)
        phone_hour = " WHERE hour='2026-01-14 17:00' AND source='iPhone'"
        if scalar(container_id,"SELECT avg_val FROM dbt_output.step_hour_state"+phone_hour) != "70":
            raise RuntimeError("raw capture overwrote the last successful hourly publication")
        dbt_incremental(dbt, dbt_env, [edit_date], "partial")
        if scalar(container_id,"SELECT avg_val FROM dbt_output.step_hour_state"+phone_hour) != "169":
            raise RuntimeError("a late sample between two previously published minutes was lost on retry")
        if scalar(container_id,"SELECT steps FROM dbt_output.daily_metrics WHERE date='2026-01-14'") != "130":
            raise RuntimeError("later Watch sample did not take priority in the recovered hour")
        load_sql(container_id, """
TRUNCATE source_data.metric_point_changes;
INSERT INTO source_data.metric_point_changes VALUES ('step_count','2026-01-14 17:00:00 +0100','iPhone',3,'ok');
""")
        dbt_incremental(dbt, dbt_env, [edit_date], "partial")
        if scalar(container_id,"SELECT avg_val FROM dbt_output.step_hour_state"+phone_hour) != "142":
            raise RuntimeError("confirmed positive replacement did not reach the hourly publication")
        load_sql(container_id, "UPDATE dbt_output.daily_metrics SET steps=432,sleep_deep=888 WHERE date='2026-01-15'")
        run([str(dbt), "run", "--full-refresh", "--select", "daily_metrics",
             "--project-dir", str(HERE), "--profiles-dir", dbt_env["DBT_PROFILES_DIR"]], env=dbt_env,cwd=ROOT)
        if scalar(container_id,"SELECT steps=432 AND sleep_deep IS DISTINCT FROM 888 FROM dbt_output.daily_metrics WHERE date='2026-01-15'") != "t":
            raise RuntimeError("full refresh lost legacy daily-only steps or retained unrelated cache fields")
        report = {"container": container_id[:12], "synthetic_marker": TOKEN,
                  "host_port": port,
                  "go_version": run(["go", "version"], cwd=ROOT, capture=True).stdout.strip(),
                  "python_version": run([sys.executable, "--version"], capture=True).stdout.strip(),
                  "dbt_version": run([str(dbt), "--version"], env=dbt_env, cwd=ROOT, capture=True).stdout.strip(),
                  "postgres": scalar(container_id, "SELECT version()"),
                  "metric_points": int(scalar(container_id, "SELECT COUNT(*) FROM source_data.metric_points")),
                  "baseline_metric_points": int(read_probe),
                  "go_full_seconds_trials_including_process_startup": go_timings,
                  "go_full_aggregate_writer_seconds_trials": go_aggregate_timings,
                  "dbt_full_seconds_trials_including_process_startup": dbt_timings,
                  "go_old_day_seconds_trials_including_process_startup": old_day_go_wall,
                  "go_old_day_aggregate_writer_seconds_trials": old_day_go_aggregate,
                  "dbt_old_day_seconds_trials_including_process_startup": old_day_dbt_wall,
                  "benchmark_dates": len(all_dates), "hourly_rows": len(actual["hourly"]),
                  "daily_rows": len(actual["daily"]), "go_matches_dbt": True,
                  "dbt_source_write_denied": True, "sum_metrics": oracle["sum_metrics"],
                  "rejected_incremental_inputs": rejected_inputs,
                  "scenario_results": phase_results,"dbt_retained_edit_retry": "passed","dbt_daily_only_full_refresh": "passed",
                  "aggregation_contract": oracle.get("aggregation_contract"),
                  "incomplete_after_complete_sleep_incremental_total": incremental_sleep,
                  "full_rebuild_after_incomplete_sleep_total": full_daily[("2026-01-15",)]["sleep_total"],
                  "full_rebuild_day1_steps_after_quality_invalidation": full_daily[("2026-01-01",)]["steps"],
                  "dbt_data_tests": "passed after benchmark trials",
                  "explain_analyze_buffers": {name: "artifacts/explain-" + name + ".txt" for name in explain_outputs}}
        (lab / "run-summary.json").write_text(json.dumps(report, indent=2) + "\n")
        with (HERE / "artifacts/.pilot-publish.lock").open("a") as lock:
            fcntl.flock(lock, fcntl.LOCK_EX)
            for artifact in [output_path, *lab.glob("explain-*.txt"), lab / "run-summary.json"]:
                artifact.replace(HERE / "artifacts" / artifact.name)
        print(json.dumps(report, indent=2))
        return 0
    finally:
        # Cleanup only the exact container created above with both matching markers.
        if container_id:
            try:
                require_owned(container_id, TOKEN)
                if docker("inspect", "-f", "{{.Id}}", container_id, capture=True).stdout.strip() == container_id:
                    docker("rm", "-f", "-v", container_id)
            except Exception:
                print(f"Cleanup unresolved; owned container {container_id} remains for manual inspection", file=sys.stderr)


if __name__ == "__main__":
    raise SystemExit(main())
