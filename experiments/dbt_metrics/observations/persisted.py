#!/usr/bin/env python3
"""Export persisted profile metrics using an explicit loopback PostgreSQL DSN.

One read-only transaction is scoped to one caller-selected profile schema. No
connection is inferred and this tool has no production endpoint/default.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import stat
import sys
import tempfile
from urllib.parse import urlparse

try:
    import psycopg2
    from psycopg2 import sql
except ImportError as error:
    raise SystemExit("Install the dbt metrics environment first (psycopg2-binary is pinned in requirements.lock).") from error

PILOT = Path(__file__).resolve().parents[1]
CATALOG_PATH = PILOT / "models/health_metric_catalog.yml"

# Only exact persisted column-to-catalog bindings are exported here.
PERSISTED = {
    "daily_scores": {
        "family": "daily", "keys": ["date"],
        "fields": ["steps", "calories", "exercise_min", "hrv_avg", "rhr_avg", "resp_avg",
                   "vo2_avg", "spo2_avg", "sleep_deep", "sleep_rem", "sleep_core", "sleep_awake",
                   "sleep_unspecified", "sleep_total"],
        "available": "computed_at",
    },
    "completed_night_sleep": {
        "family": "main_night", "keys": ["wake_date", "source", "source_epoch", "algorithm_version"],
        "fields": ["duration_hours", "capture_completeness", "duration_assessment", "finalization_state",
                   "claim_eligibility", "observed_at", "finalized_at", "covered_interval_start", "covered_interval_end"],
        "available": None,
    },
    "workouts": {
        "family": "workout", "keys": ["id", "external_id", "name", "origin", "source_snapshot_at"],
        "fields": ["name", "start_time", "end_time", "duration_sec", "is_indoor", "avg_hr_bpm", "max_hr_bpm",
                   "energy_kcal", "intensity", "distance_km", "avg_speed_kmh", "max_speed_kmh", "elevation_up_m",
                   "step_count_total", "step_cadence_spm", "temperature_c", "humidity_pct", "hr_z1_sec", "hr_z2_sec",
                   "hr_z3_sec", "hr_z4_sec", "hr_z5_sec"],
        "available": "received_at",
    },
    "energy_snapshots": {
        "family": "energy_snapshot", "keys": ["ts_bucket", "date"],
        "fields": ["bank", "drain_delta", "restore_delta", "formula_version", "components", "flags"],
        "available": "computed_at",
    },
    "target_snapshots": {
        "family": "target", "keys": ["date", "sub_score", "target_kind", "source_epoch"],
        "fields": ["target_value", "eligible", "eligibility_reason", "data_coverage", "formula_version"],
        "available": "computed_at",
    },
}


def catalog():
    doc = json.loads(CATALOG_PATH.read_text())
    return doc["models"][0]["config"]["meta"]["health_catalog"]


def read_rows(cur, schema: str, relation: str, columns: list[str], limit: int):
    projection = sql.SQL(",").join(sql.Identifier(col) for col in columns)
    query = sql.SQL("SELECT to_jsonb(t) FROM (SELECT {} FROM {} LIMIT %s) AS t").format(
        projection, sql.Identifier(schema, relation))
    cur.execute(query, (limit + 1,))
    rows = [row[0] for row in cur.fetchall()]
    if len(rows) > limit:
        raise RuntimeError(f"{relation} exceeded --limit={limit}; narrow the local fixture or raise the explicit limit")
    return rows


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--schema", required=True, help="one profile schema; transaction search_path is restricted to it")
    parser.add_argument("--profile", required=True, help="non-sensitive synthetic/profile label stored in export")
    parser.add_argument("--output", required=True, type=Path, help="JSON destination in an existing private directory")
    parser.add_argument("--limit", type=int, default=10000, help="maximum rows per relation (default 10000)")
    args = parser.parse_args()
    if args.limit < 1 or not args.schema.replace("_", "").isalnum():
        parser.error("--limit must be positive and --schema must be a simple identifier")
    if not args.output.parent.is_dir() or args.output.is_symlink():
        parser.error("--output parent must exist and destination must not be a symlink")
    if stat.S_IMODE(args.output.parent.stat().st_mode) & 0o077:
        parser.error("--output parent must be private (mode 0700 or stricter)")
    dsn = os.environ.get("HEALTH_METRIC_READ_DSN")
    parsed = urlparse(dsn or "")
    if (parsed.scheme not in {"postgres", "postgresql"} or parsed.hostname not in {"localhost", "127.0.0.1", "::1"}
            or any(name in os.environ for name in ("PGHOST", "PGHOSTADDR", "PGSERVICE", "PGSERVICEFILE"))):
        parser.error("set HEALTH_METRIC_READ_DSN to an explicitly authorized loopback PostgreSQL DSN")
    try:
        parameters = psycopg2.extensions.parse_dsn(dsn)
    except psycopg2.Error:
        parser.error("HEALTH_METRIC_READ_DSN is not a valid local DSN")
    if (parameters.get("host") not in {"localhost", "127.0.0.1", "::1"}
            or parameters.get("hostaddr") not in {None, "localhost", "127.0.0.1", "::1"}
            or "service" in parameters or "servicefile" in parameters
            or not {"dbname", "user"}.issubset(parameters)):
        parser.error("DSN must specify a loopback host, database and user, without service routing")

    c = catalog()
    observation_defs = c["observations"]
    records = []
    try:
        conn = psycopg2.connect(dsn, connect_timeout=5)
        conn.set_session(readonly=True, autocommit=False)
        with conn:
            with conn.cursor() as cur:
                cur.execute("SET LOCAL statement_timeout = '30s'")
                cur.execute(sql.SQL("SET LOCAL search_path = {}, pg_catalog").format(sql.Identifier(args.schema)))
                cur.execute("SELECT current_schema(), current_setting('transaction_read_only')")
                actual_schema, readonly = cur.fetchone()
                if actual_schema != args.schema or readonly != "on":
                    raise RuntimeError("profile search_path or read-only transaction guard failed")
                cur.execute("SELECT to_regclass(%s)", (f"{args.schema}.metric_points",))
                if cur.fetchone()[0] is None:
                    raise RuntimeError("selected profile has no metric_points relation")

                raw_columns = ["id", "health_record_id", "received_at", "metric_name", "units", "date", "qty", "source", "origin", "source_snapshot_at"]
                query = sql.SQL("SELECT to_jsonb(p) FROM (SELECT {} FROM {} WHERE metric_name = ANY(%s) LIMIT %s) AS p").format(
                    sql.SQL(",").join(sql.Identifier(col) for col in raw_columns),
                    sql.Identifier(args.schema, "metric_points"))
                cur.execute(query, (list(observation_defs), args.limit + 1))
                raw_rows = [row[0] for row in cur.fetchall()]
                if len(raw_rows) > args.limit:
                    raise RuntimeError("metric_points exceeded --limit")
                for row in raw_rows:
                    metric_id = row["metric_name"]
                    observed, available = row.get("date"), row.get("received_at")
                    records.append({"metric_id": metric_id,
                                    "key": {"id": row["id"], "original_date": observed, "source": row["source"],
                                            "units": row["units"], "origin": row["origin"],
                                            "health_record_id": row["health_record_id"],
                                            "source_snapshot_at": row["source_snapshot_at"]},
                                    "value": row["qty"], "source": f"{args.schema}.metric_points",
                                    "profile": args.profile, "observed_at": observed, "available_at": available,
                                    "catalog_units": _observation_units(observation_defs[metric_id])})

                for relation, spec in PERSISTED.items():
                    cur.execute("SELECT to_regclass(%s)", (f"{args.schema}.{relation}",))
                    if cur.fetchone()[0] is None:
                        continue
                    required = set(spec["keys"] + spec["fields"] + ([spec["available"]] if spec["available"] else []))
                    cur.execute("SELECT column_name FROM information_schema.columns WHERE table_schema=%s AND table_name=%s",
                                (args.schema, relation))
                    missing = required - {row[0] for row in cur.fetchall()}
                    if missing:
                        raise RuntimeError(f"{args.schema}.{relation} missing required columns: {', '.join(sorted(missing))}")
                    rows = read_rows(cur, args.schema, relation, sorted(required), args.limit)
                    family = c["families"].get(spec["family"])
                    for row in rows:
                        key = {field: row[field] for field in spec["keys"]}
                        observed = row.get("observed_at") or row.get("start_time") or row.get("ts_bucket") or row.get("date")
                        available = row.get(spec["available"]) if spec["available"] else None
                        for field in spec["fields"]:
                            metric_id = f"{spec['family']}.{field}"
                            if relation == "daily_scores":
                                definition = c["contracts"].get(metric_id)
                                if not definition or definition.get("source_keys") != [f"daily_scores.{field}"]:
                                    raise RuntimeError(f"catalog column mapping missing for {metric_id}")
                            else:
                                definition = family["fields"].get(field)
                                if definition is None:
                                    raise RuntimeError(f"catalog mapping missing for {metric_id}")
                            units = definition.get("units", ["unknown"])
                            if relation == "target_snapshots" and field == "target_value":
                                candidate = f"{row['sub_score']}.{row['target_kind']}"
                                if candidate in c["contracts"]:
                                    metric_id = candidate
                                    units = c["contracts"][candidate].get("units", units)
                            record = {"metric_id": metric_id,
                                            "key": key | {"field": field}, "value": row[field],
                                            "source": f"{args.schema}.{relation}", "profile": args.profile,
                                            "observed_at": observed, "available_at": available,
                                            "catalog_units": units,
                                            "version": row.get("algorithm_version", row.get("formula_version"))}
                            records.append(record)
                            if relation == "target_snapshots" and field == "target_value" and metric_id != "target.target_value":
                                records.append(record | {"metric_id": "target.target_value",
                                                         "catalog_units": family["fields"]["target_value"].get("units", ["unknown"])})
        conn.close()
    except psycopg2.Error:
        raise RuntimeError("local read failed; database diagnostics redacted") from None

    fd, temporary = tempfile.mkstemp(prefix=".persisted-", suffix=".json", dir=args.output.parent)
    try:
        os.fchmod(fd, 0o600)
        with os.fdopen(fd, "w") as stream:
            stream.write(json.dumps(records, ensure_ascii=False, indent=2, default=str) + "\n")
        os.replace(temporary, args.output)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)
    print(f"PASS: exported {len(records)} read-only persisted metric records from {args.schema}")
    print(f"Output: {args.output}")
    return 0


def _observation_units(definition):
    units = set()
    for name in ("xml", "ios"):
        if name in definition:
            units.add(definition[name][1])
    if "xml_categories" in definition:
        units.add("hr")
    if "xml_special_unit" in definition:
        units.add(definition["xml_special_unit"])
    return sorted(units) if units else ["unknown"]


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as error:
        print(f"FAIL: {error}", file=sys.stderr)
        raise SystemExit(1)
