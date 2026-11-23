#!/usr/bin/env python3
"""Read each catalogued raw observation from two isolated synthetic schemas."""
from __future__ import annotations

import json
import os
from pathlib import Path
import subprocess
import stat
import sys
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[3]
PILOT = Path(__file__).resolve().parents[1]
HERE = Path(__file__).resolve().parent
CATALOG = PILOT / "models/health_metric_catalog.yml"
IMAGE = "postgres:16-alpine"
PASSWORD = "synthetic-only"


def run(args: list[str], *, input_text: str | None = None, capture: bool = False,
        env: dict[str, str] | None = None, check: bool = True) -> str:
    result = subprocess.run(args, input=input_text, text=True, check=check,
                            capture_output=True, cwd=ROOT, env=env)
    if not capture and result.stdout:
        print(result.stdout, end="")
    return result.stdout.strip()


def catalog_rows() -> list[dict]:
    document = json.loads(CATALOG.read_text())
    catalog = document["models"][0]["config"]["meta"]["health_catalog"]
    defaults = catalog["observation_defaults"]
    rows = []
    for metric_id, evidence in catalog["observations"].items():
        units: set[str] = set()
        for field in ("xml", "ios"):
            if field in evidence:
                units.add(evidence[field][1])
        if "xml_categories" in evidence:
            units.add("hr")
        if "xml_special_unit" in evidence:
            units.add(evidence["xml_special_unit"])
        ios = evidence.get("ios")
        rows.append({"metric_id": metric_id, "units": sorted(units) if units else ["unknown"],
                     "grain": ios[2] if ios and len(ios) > 2 else defaults["grain"],
                     "value_type": defaults["value_type"]})
    ids = [row["metric_id"] for row in rows]
    if len(set(ids)) != len(ids) or not rows:
        raise RuntimeError("catalog observation IDs must be nonempty and unique")
    return rows


def q(value: str) -> str:
    return "'" + value.replace("'", "''") + "'"


def main() -> int:
    definitions = catalog_rows()
    container = "health-observations-" + uuid.uuid4().hex[:12]
    output_dir = os.environ.get("HEALTH_METRIC_AUDIT_DIR")
    if not output_dir:
        raise RuntimeError("HEALTH_METRIC_AUDIT_DIR must name an existing private output directory")
    out = Path(output_dir)
    if not out.is_dir():
        raise RuntimeError("HEALTH_METRIC_AUDIT_DIR must already exist")
    if stat.S_IMODE(out.stat().st_mode) & 0o077:
        raise RuntimeError("HEALTH_METRIC_AUDIT_DIR must be private (mode 0700 or stricter)")
    profiles = [("profile_a", "health_observation_a", "Synthetic A", 0),
                ("profile_b", "health_observation_b", "Synthetic B", 1000)]
    started = False
    try:
        run(["docker", "run", "-d", "--rm", "--name", container,
             "-e", f"POSTGRES_PASSWORD={PASSWORD}", "-p", "127.0.0.1::5432", IMAGE], capture=True)
        started = True
        for _ in range(60):
            ready = subprocess.run(["docker", "exec", "-e", f"PGPASSWORD={PASSWORD}", container,
                                    "pg_isready", "-U", "postgres"], text=True, capture_output=True)
            if ready.returncode == 0:
                break
            import time
            time.sleep(0.5)
        else:
            logs = subprocess.run(["docker", "logs", container], text=True, capture_output=True)
            raise RuntimeError("synthetic PostgreSQL did not become ready: " + logs.stderr[-1000:] + logs.stdout[-1000:])

        admin = ["docker", "exec", "-i", "-e", f"PGPASSWORD={PASSWORD}", container,
                 "psql", "-q", "-v", "ON_ERROR_STOP=1", "-U", "postgres", "-d", "postgres"]
        setup = []
        for schema, role, _, _ in profiles:
            setup += [f"CREATE ROLE {role} LOGIN PASSWORD {q(PASSWORD)} NOSUPERUSER NOCREATEDB NOCREATEROLE;",
                      f"CREATE SCHEMA {schema} AUTHORIZATION {role};"]
        run(admin, input_text="\n".join(setup))

        for schema, role, label, offset in profiles:
            sql = [f"SET ROLE {role}; SET search_path TO {schema};",
                   "CREATE TABLE health_records (id bigint primary key); INSERT INTO health_records VALUES (1);",
                   "CREATE TABLE metric_points (id bigserial primary key, health_record_id bigint not null references health_records(id), received_at timestamptz default now(), metric_name text not null, units text, date text not null, qty real, source text, origin text not null default 'live', import_run_id bigint, source_snapshot_at timestamptz);",
                   "CREATE TABLE observation_catalog (metric_id text primary key, catalog_units jsonb not null, catalog_grain text not null, value_type text not null);"]
            for item in definitions:
                sql.append("INSERT INTO observation_catalog VALUES (" +
                           f"{q(item['metric_id'])},{q(json.dumps(item['units']))}::jsonb," +
                           f"{q(item['grain'])},{q(item['value_type'])});")
            sql.append((HERE / "read.sql").read_text())
            for i, item in enumerate(definitions):
                unit = item["units"][0] if item["units"] != ["unknown"] else "synthetic_unit"
                value = "NULL" if schema == "profile_b" and i == 0 else str(offset + i + 0.25)
                date = ("2026-11-19 07:03:04.123456 +0100" if schema == "profile_a"
                        else "2026-11-19 06:03:04.123456 +0000")
                sql.append("INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,origin,received_at,source_snapshot_at) VALUES (1," +
                           f"{q(item['metric_id'])},{q(unit)},{q(date)},{value},{q(label)},'synthetic'," +
                           "'2026-11-22 11:00:00+00','2026-11-22 10:00:00+00');")
            sql += [f"GRANT USAGE ON SCHEMA {schema} TO {role};",
                    f"GRANT SELECT ON metric_points, observation_catalog, health_observation_v1 TO {role};",
                    "RESET ROLE;"]
            run(admin, input_text="\n".join(sql))

        daily_values = {
            "steps": (0, "count"), "calories": (350.5, "kcal"), "exercise_min": (32, "min"),
            "hrv_avg": (47.5, "ms"), "rhr_avg": (61, "bpm"), "resp_avg": (14.25, "count/min"),
            "vo2_avg": (None, "mL/kg/min"), "spo2_avg": (97.5, "%"), "sleep_deep": (1.5, "hr"),
            "sleep_rem": (2, "hr"), "sleep_core": (4, "hr"), "sleep_awake": (0.25, "hr"),
            "sleep_unspecified": (None, "hr"), "sleep_total": (7.5, "hr"),
        }
        daily_date = "2026-11-19"
        daily_clock = "2026-11-20 07:04:03.125+01"
        daily_ddl = ", ".join(f"{field} real" for field in daily_values)
        daily_row = ", ".join("null" if value is None else str(value) for value, _ in daily_values.values())
        saved_fixtures = [
            ("profile_daily", "health_daily", "daily_scores",
             f"CREATE TABLE daily_scores(date text primary key,computed_at text,score_version int,{daily_ddl});"
             f"INSERT INTO daily_scores VALUES ({q(daily_date)},{q(daily_clock)},17,{daily_row});", "daily.steps"),
            ("profile_sleep", "health_sleep", "completed_night_sleep",
             "CREATE TABLE completed_night_sleep (wake_date text primary key,duration_hours double precision not null,source text not null,source_epoch text not null,input_hash text not null,capture_completeness text not null,duration_assessment text not null,finalization_state text not null,claim_eligibility text not null,observed_at timestamptz not null,finalized_at timestamptz,algorithm_version text not null,covered_interval_start timestamptz,covered_interval_end timestamptz,diagnostics jsonb not null default '{}'::jsonb); INSERT INTO completed_night_sleep(wake_date,duration_hours,source,source_epoch,input_hash,capture_completeness,duration_assessment,finalization_state,claim_eligibility,observed_at,finalized_at,algorithm_version) VALUES ('2026-11-19',7.5,'Synthetic Watch','epoch-a','hash-a','complete','plausible','final','eligible','2026-11-19T08:00:00+01','2026-11-19T09:00:00+01','v1');",
             "main_night.duration_hours"),
            ("profile_workout", "health_workout", "workouts",
             "CREATE TABLE workouts (id bigint primary key,external_id text not null,name text not null,start_time timestamptz not null,end_time timestamptz not null,duration_sec double precision not null,is_indoor boolean not null,avg_hr_bpm double precision,max_hr_bpm double precision,energy_kcal double precision,intensity double precision,distance_km double precision,avg_speed_kmh double precision,max_speed_kmh double precision,elevation_up_m double precision,step_count_total integer,step_cadence_spm double precision,temperature_c double precision,humidity_pct double precision,hr_z1_sec integer,hr_z2_sec integer,hr_z3_sec integer,hr_z4_sec integer,hr_z5_sec integer,origin text not null,source_snapshot_at timestamptz,received_at timestamptz not null); INSERT INTO workouts(id,external_id,name,start_time,end_time,duration_sec,is_indoor,avg_hr_bpm,origin,source_snapshot_at,received_at) VALUES (1,'w-1','Synthetic run','2026-11-19T06:00:00+01','2026-11-19T06:30:00+01',1800,false,125,'synthetic','2026-11-19T06:35:00+01','2026-11-19T06:36:00+01');",
             "workout.start_time"),
            ("profile_energy", "health_energy", "energy_snapshots",
             "CREATE TABLE energy_snapshots (ts_bucket timestamptz primary key,date text not null,bank integer not null,drain_delta integer not null,restore_delta integer not null,formula_version integer not null,components jsonb,flags text[] not null default '{}',computed_at timestamptz not null); INSERT INTO energy_snapshots VALUES ('2026-11-19T23:55:00+01','2026-11-19',68,-3,1,3,'{}','{}','2026-11-19T23:56:00+01');",
             "energy_snapshot.bank"),
            ("profile_target", "health_target", "target_snapshots",
             "CREATE TABLE target_snapshots (date text not null,sub_score text not null,target_kind text not null,target_value real,eligible boolean not null,eligibility_reason text not null,data_coverage jsonb,source_epoch text not null,formula_version integer not null,computed_at timestamptz not null,primary key(date,sub_score,target_kind)); INSERT INTO target_snapshots VALUES ('2026-11-19','recovery_stability','target_daily_point',72.5,true,'eligible','{}','epoch-a',1,'2026-11-19T08:01:00+01');",
             "recovery_stability.target_daily_point"),
        ]
        fixture_roles = []
        for schema, role, relation, fixture_sql, expected_id in saved_fixtures:
            fixture_roles.append((schema, role, relation, expected_id))
            fixture_sql = (f"CREATE ROLE {role} LOGIN PASSWORD {q(PASSWORD)} NOSUPERUSER NOCREATEDB NOCREATEROLE; "
                           f"CREATE SCHEMA {schema} AUTHORIZATION {role}; SET ROLE {role}; SET search_path TO {schema}; "
                           "CREATE TABLE health_records(id bigint primary key); CREATE TABLE metric_points(id bigserial primary key,health_record_id bigint not null references health_records(id),received_at timestamptz default now(),metric_name text not null,units text,date text not null,qty real,source text,origin text not null default 'live',import_run_id bigint,source_snapshot_at timestamptz); "
                           + fixture_sql + f" GRANT USAGE ON SCHEMA {schema} TO {role}; GRANT SELECT ON metric_points,{relation} TO {role}; RESET ROLE;")
            run(admin, input_text=fixture_sql)

        records = []
        for schema, role, label, _ in profiles:
            query = ("SELECT COALESCE(json_agg(json_build_object('metric_id',observation_id,"
                     "'key',json_build_object('original_date',original_date,'source',source,'units',units,"
                     "'origin',origin,'received_at',received_at,'source_snapshot_at',source_snapshot_at),"
                     "'value',value,'source','" + schema + ".health_observation_v1','profile','" + schema + "') "
                     "ORDER BY observation_id),'[]'::json)::text FROM " + schema + ".health_observation_v1")
            output = run(["docker", "exec", "-e", f"PGPASSWORD={PASSWORD}", container,
                          "psql", "-At", "-U", role, "-d", "postgres", "-c", query], capture=True)
            rows = json.loads(output)
            by_id = {row["metric_id"]: row for row in rows}
            if len(rows) != len(definitions) or set(by_id) != {d["metric_id"] for d in definitions}:
                raise RuntimeError(f"{schema}: expected one row per catalog ID")
            expected_date = ("2026-11-19 07:03:04.123456 +0100" if schema == "profile_a"
                             else "2026-11-19 06:03:04.123456 +0000")
            for i, definition in enumerate(definitions):
                row = by_id[definition["metric_id"]]
                key = row["key"]
                if key["original_date"] != expected_date or key["source"] != label or key["origin"] != "synthetic":
                    raise RuntimeError(f"{schema}/{definition['metric_id']}: source clock/provenance changed")
                if not key["received_at"].startswith("2026-11-22T11:00:00") or not key["source_snapshot_at"].startswith("2026-11-22T10:00:00"):
                    raise RuntimeError(f"{schema}/{definition['metric_id']}: source metadata changed")
                if row["value"] is None:
                    if not (schema == "profile_b" and i == 0):
                        raise RuntimeError(f"{schema}/{definition['metric_id']}: unexpected missing value")
                elif not isinstance(row["value"], (int, float)):
                    raise RuntimeError(f"{schema}/{definition['metric_id']}: expected numeric JSON value")
                if key["units"] not in definition["units"] and definition["units"] != ["unknown"]:
                    raise RuntimeError(f"{schema}/{definition['metric_id']}: unit mismatch")
                records.append({"metric_id": row["metric_id"], "key": key, "value": row["value"],
                                "source": row["source"], "profile": row["profile"]})
            missing = run(["docker", "exec", "-e", f"PGPASSWORD={PASSWORD}", container,
                           "psql", "-At", "-U", role, "-d", "postgres", "-c",
                           f"SELECT count(*) FROM {schema}.health_observation_v1 WHERE observation_id='not_in_catalog'"], capture=True)
            if missing != "0":
                raise RuntimeError(f"{schema}: unknown observation ID should return no rows")

        denied = subprocess.run(["docker", "exec", "-e", f"PGPASSWORD={PASSWORD}", container,
                                 "psql", "-v", "ON_ERROR_STOP=1", "-U", profiles[0][1], "-d", "postgres",
                                 "-c", "SELECT * FROM profile_b.health_observation_v1 LIMIT 1"],
                                text=True, capture_output=True)
        if denied.returncode == 0:
            raise RuntimeError("cross-profile read unexpectedly succeeded")
        if len(records) != len(definitions) * len(profiles):
            raise RuntimeError("audit record count mismatch")
        write_private_json(out / "observations.json", records)
        mapped_port = run(["docker", "port", container, "5432/tcp"], capture=True).rsplit(":", 1)[1]
        exporter = ROOT / "experiments/dbt_metrics/observations/persisted.py"
        python = PILOT / ".venv/bin/python"
        if not python.is_file():
            raise RuntimeError("install experiments/dbt_metrics requirements before running persisted exporter check")
        for schema, role, relation, expected_id in fixture_roles:
            persisted_path = out / f"persisted_{relation}.json"
            dsn = f"postgresql://{role}:{PASSWORD}@127.0.0.1:{mapped_port}/postgres?sslmode=disable"
            exporter_env = os.environ.copy()
            exporter_env["HEALTH_METRIC_READ_DSN"] = dsn
            exported = subprocess.run([str(python), str(exporter), "--schema", schema,
                                       "--profile", schema, "--output", str(persisted_path)],
                                      cwd=ROOT, env=exporter_env, text=True, capture_output=True)
            if exported.returncode:
                raise RuntimeError("persisted exporter failed: " + exported.stderr[-1500:])
            persisted = json.loads(persisted_path.read_text())
            ids = {row["metric_id"] for row in persisted}
            if relation == "daily_scores":
                expected_ids = {f"daily.{field}" for field in daily_values}
                if ids != expected_ids or len(persisted) != len(expected_ids):
                    raise RuntimeError("daily export does not cover each of the 14 columns exactly once")
                for record in persisted:
                    field = record["metric_id"].split(".", 1)[1]
                    value, unit = daily_values[field]
                    if (record["value"] != value or record["catalog_units"] != [unit]
                            or record["version"] is not None or record["available_at"] != daily_clock
                            or record["observed_at"] != daily_date or record["profile"] != schema
                            or record["source"] != f"{schema}.daily_scores"
                            or record["key"] != {"date": daily_date, "field": field}):
                        raise RuntimeError(f"incorrect daily persisted record for {field}")
                load_sql = run([sys.executable, str(PILOT / "metric_schema/profile_sql.py"),
                                "--persisted", str(persisted_path), "--profile", schema,
                                "--schema", "daily_research"], capture=True)
                run(admin, input_text=load_sql)
                summary = run([*admin, "-At"], input_text="""
                    SELECT count(*), count(*) FILTER (WHERE value_status='missing'),
                           count(*) FILTER (WHERE numeric_value=0),
                           count(*) FILTER (WHERE available_at='2026-11-20 07:04:03.125+01'
                                             AND source_version IS NULL)
                    FROM daily_research.metric_read_v1 WHERE metric_id LIKE 'daily.%' AND source IS NOT NULL
                """, capture=True)
                if summary != "14|2|1|14":
                    raise RuntimeError("research view lost daily rollup values or provenance")
            if expected_id not in ids:
                raise RuntimeError(f"persisted exporter missed {expected_id} from {relation}")
            if stat.S_IMODE(persisted_path.stat().st_mode) != 0o600:
                raise RuntimeError("persisted exporter did not restrict audit file permissions")
        print(f"PASS: {len(definitions)} catalog IDs read through one path in each of {len(profiles)} isolated profiles")
        print(f"PASS: {len(daily_values)} persisted daily rollups queried through metric_read_v1")
        print(f"Audit output: {out / 'observations.json'}")
        return 0
    finally:
        if started:
            subprocess.run(["docker", "rm", "-f", container], text=True, capture_output=True)


def write_private_json(path: Path, records: list[dict]) -> None:
    fd, temporary = tempfile.mkstemp(prefix=".observations-", suffix=".json", dir=path.parent)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w") as stream:
            stream.write(json.dumps(records, ensure_ascii=False, indent=2) + "\n")
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(f"FAIL: {error}", file=sys.stderr)
        raise SystemExit(1)
