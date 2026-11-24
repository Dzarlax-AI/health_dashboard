#!/usr/bin/env python3
"""Verify the installed tenant catalog on an operator-supplied offline copy.

Use standard libpq environment variables for the connection. Prints only
per-metric counts, comparison outcomes and SQLSTATEs. No data are written.
"""
import argparse
import hashlib
import json
from pathlib import Path

import psycopg2

from check_semantic_catalog import definitions, read_catalog
from native_archive_nights import check_nights
from native_catalog_vars import native_catalog_vars
from verify_archive_hrv import reference_daily_sql
from verify_catalog_archive import JSON_MODELS, STORED, archived_values, ident, observations

PILOT = Path(__file__).resolve().parents[1]


def source_digest():
    paths = sorted([*PILOT.glob("models/**/*.sql"), *PILOT.glob("macros/**/*.sql"),
                    PILOT / "models/health_metric_catalog.yml",
                    PILOT / "scripts/native_catalog_vars.py", PILOT / "scripts/profile_graph_catalog.py"])
    digest = hashlib.sha256()
    for path in paths:
        digest.update(str(path.relative_to(PILOT)).encode() + b"\0" + path.read_bytes() + b"\0")
    return digest.hexdigest()


def canonical_night_checks(schema_name, view):
    schema = ident(schema_name)
    calendar = f"(select date from {schema}.daily_scores union select wake_date from {schema}.completed_night_sleep)"
    day_model = "semantic_sleep_day_v1"
    for col in ("duration_hours", "duration_assessment", "capture_completeness", "claim_eligibility", "finalization_state"):
        metric = "main_night." + col
        expected = f"""select jsonb_build_object('wake_date',c.date) k,to_jsonb(n.{ident(col)}) v
            from {calendar} c left join {schema}.completed_night_sleep n on n.wake_date=c.date"""
        yield metric, key_value_comparison(metric, day_model, expected, view)
    # Use the existing independent priority-table oracle for the 30-day calendar.
    sql = (PILOT / "scripts/verify_archive_sleep_history.sql").read_text()
    ctes, separator, _ = sql.partition("select json_build_object")
    if not separator:
        raise ValueError("unrecognized sleep-history oracle")
    ctes = ctes.replace("source_data.calendar", calendar).replace("source_data.completed_night_sleep", schema + ".completed_night_sleep")
    expected = ctes + "select jsonb_build_object('wake_date',date,'history_date',historical) k,to_jsonb(reason) v from expected"
    metric = "sleep_reference_day_decision_v1"
    yield metric, key_value_comparison(metric, "semantic_sleep_history_v1", expected, view)


def native_hrv_checks(schema_name, view):
    schema = ident(schema_name)
    points = f"{schema}.metric_points"
    calendar = f"(select date from {schema}.daily_scores union select left(date,10) from {points} where metric_name='heart_rate_variability')"
    cache = f"""(select d.date,d.hrv_avg,exists(select from {schema}.settings where
        key='cache_maintenance_hrv_repair_v1:'||d.date) as repair_pending from {schema}.daily_scores d)"""
    reference = reference_daily_sql().replace("source_data.metric_points", points).replace("source_data.calendar", calendar).replace("source_data.daily_hrv", cache)
    for metric, column in (("hrv_fixed_clock_day_median_v1","fixed"), ("hrv_morning_compat_v1","morning"),
                           ("hrv_daily_live_compat_v1","recalculated"), ("hrv_daily_legacy_compat_v1","recalculated")):
        expected = f"select jsonb_build_object('date',date) k,to_jsonb({column}) v from ({reference}) ref"
        yield metric, key_value_comparison(metric, "semantic_hrv_daily_v1", expected, view)
    metric = "hrv_observation_v1"
    expected = f"""select jsonb_build_object('date',date,'source',coalesce(source,'')) k,to_jsonb(qty) v
        from {points} where metric_name='heart_rate_variability'"""
    yield metric, key_value_comparison(metric, "semantic_hrv_observation_v1", expected, view)


def untyped_hrv_checks(cur, schema, view):
    # Absence of explicit methods is independently provable on old inputs.
    # When any receipt has a marker, its decoding needs a separate positive oracle.
    cur.execute(f"""select exists(select from {schema}.health_records where
        (content_type='apple-health-import; catalog=1' and payload ~ '^import_run_id=[0-9]+$')
        or payload ~ '"(method|identifier)"[[:space:]]*:')""")
    if cur.fetchone()[0]:
        return
    for method in ("unknown", "sdnn", "rmssd"):
        metric = f"hrv_observation_{method}_v1"
        expected = f"""select jsonb_build_object('date',date,'source',coalesce(source,'')) k,to_jsonb(qty) v
            from {schema}.metric_points where metric_name='heart_rate_variability' and '{method}'='unknown'"""
        yield metric, key_value_comparison(metric, "semantic_hrv_observation_v1", expected, view)


def oxygen_workout_checks(schema, view):
    reference, separator, _ = (PILOT / "scripts/verify_archive_oxygen.sql").read_text().partition(", compared as (")
    if not separator:
        raise ValueError("unrecognized oxygen oracle")
    reference = reference.replace("source_data.metric_points", schema + ".metric_points")
    for col in ("status", "valid_readings", "below94_count", "below92_count", "min", "avg"):
        metric = "oxygen_cluster." + col
        expected = reference + f" select jsonb_build_object('profile',current_user,'capture',capture) k,to_jsonb({ident(col)}) v from reference"
        yield metric, key_value_comparison(metric, "oxygen_cluster_v1", expected, view, tolerance=1e-12 if col=="avg" else None)
    for col, agg in (("count","count(*)"), ("first_seen","min(start_time)"), ("last_seen","max(start_time)")):
        metric = "workout_type." + col
        expected = f"""select jsonb_build_object('profile',current_user,'name',name) k,to_jsonb({agg}) v
            from {schema}.workouts group by name"""
        yield metric, key_value_comparison(metric, "workout_type_v1", expected, view)


def shadow_night_checks(schema, view):
    # The independent oracle sums disjoint adjacent boundary intervals. The
    # production model uses PostgreSQL multiranges to merge overlapping stages.
    sql = (PILOT / "scripts/verify_archive_shadow_sleep.sql").read_text()
    reference, separator, _ = sql.partition(", joined as (")
    if not separator:
        raise ValueError("unrecognized shadow-night oracle")
    candidates = f"""(select * from {schema}.sleep_rollout_candidates
        where profile=(select tenant_id from {schema}.__tenant_identity where singleton)
        and not snapshot ? 'withdrawal' and not snapshot ? 'coverage') source_candidates"""
    reference = reference.replace("source_data.sleep_rollout_candidates", candidates)
    reference = reference.replace("select w.wake_date,w.id,w.revision", "select w.profile,w.wake_date,w.id,w.revision")
    base_key = "jsonb_build_object('profile',profile,'wake_date',wake_date)"
    for metric, value in (("shadow_sleep_daily_duration_v1","coalesce(duration,case when zero ? 'total' then 0 end)::float8"),
                          ("shadow_sleep_daily_stage_conflict_v1","conflict")):
        expected = reference + f" select {base_key} k,to_jsonb({value}) v from outputs"
        yield metric, key_value_comparison(metric, "shadow_sleep_daily_v1", expected, view)
    expected = reference + f"""select {base_key} || jsonb_build_object('component',m.col) k,
        to_jsonb((case when not conflict then coalesce(m.seconds,case when zero ? m.kind then 0 end) end)::float8) v
        from outputs cross join lateral (values ('deep','deep_seconds',deep),('rem','rem_seconds',rem),
        ('core','core_seconds',core),('asleep_unspecified','unspecified_seconds',unspecified),
        ('awake','awake_seconds',awake)) m(kind,col,seconds)"""
    metric = "shadow_sleep_daily_stages_v1"
    yield metric, key_value_comparison(metric, "shadow_sleep_daily_v1", expected, view)


def key_value_comparison(metric, model, expected, view, tolerance=None):
    difference = "e.v is distinct from c.v"
    if tolerance is not None:
        difference = f"((e.v is null) <> (c.v is null) or abs((e.v::text)::float8-(c.v::text)::float8)>{tolerance})"
    return f"""with expected_rows as ({expected}), candidate as (
        select sample_key k,value v from {view} where source='{model}' and metric_id='{metric}')
        select json_build_array('{metric}',count(e.k),count(c.k),count(distinct c.k),
            count(*) filter(where e.k is null or c.k is null or {difference}))
        from expected_rows e full join candidate c using(k)"""


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--schema", required=True)
    parser.add_argument("--timeout-seconds", type=int, default=120)
    parser.add_argument("--cache-night-reads", action="store_true",
                        help="reuse one actual catalog query for full-history night checks in a session-local temporary table")
    parser.add_argument("--checkpoint", type=Path, help="write aggregate progress to this local file")
    args = parser.parse_args()
    schema = ident(args.schema)
    view = f"{schema}.tenant_native_metric_read_v1"
    if args.timeout_seconds <= 0:
        parser.error("timeout must be positive")
    variables = native_catalog_vars(args.schema)
    types = {d["metric_id"]: d["value_type"] for d in definitions(read_catalog())}
    status, oracle, errors = {}, {}, {}
    def checkpoint():
        if args.checkpoint:
            temp = args.checkpoint.with_suffix(".tmp")
            temp.write_text(json.dumps(dict(status=status, oracle=oracle, errors=errors)))
            temp.replace(args.checkpoint)

    connection = psycopg2.connect("")
    try:
        connection.autocommit = True
        with connection.cursor() as cur:
            if args.cache_night_reads:
                cur.execute("create temporary table archive_night_rows(metric_id text,source text,sample_key jsonb,"
                            "value jsonb,value_status text,source_metadata jsonb)")
                cur.execute("create index on archive_night_rows(metric_id)")
            cur.execute("set default_transaction_read_only=on")
            cur.execute(f"set search_path={schema},pg_catalog")
            cur.execute("set timezone='UTC'")
            cur.execute("set jit=off")
            cur.execute("set statement_timeout=%s", (args.timeout_seconds * 1000,))
            cur.execute("select current_schema(),rolsuper from pg_roles where rolname=current_user")
            if cur.fetchone() != (args.schema, False):
                raise ValueError("use a restricted role with access to the archive schema")

            cached_ids = set()
            cache = "pg_temp.archive_night_rows"
            if args.cache_night_reads:
                sources = ("semantic_hrv_night_v1", "semantic_sleep_continuity_v1",
                           "semantic_sleep_quality_v1", "semantic_sleep_day_v1")
                # Read genuine rows from the installed view. The temporary target can
                # be filled in read-only mode. Restrict reuse to IDs with no other source.
                cur.execute(f"insert into {cache} select metric_id,source,sample_key,value,value_status,source_metadata "
                            f"from {view} where source in %s", (sources,))
                cur.execute(f"select distinct metric_id from {cache}")
                present = {row[0] for row in cur.fetchall()}
                other_ids = set(variables["profile_graph_observations"])
                for group in variables["profile_graph_models"]:
                    if group["model"] not in sources:
                        other_ids.update(field["metric"] for field in group["fields"])
                cached_ids = present - other_ids

            def compare(key, query):
                if key in cached_ids:
                    query = query.replace(view, cache)
                try:
                    cur.execute(query)
                    _, expected, actual, unique, different = cur.fetchone()[0]
                    oracle[key] = [expected, actual, unique, different]
                    if expected != actual or actual != unique or different:
                        errors["values/" + key] = "different"
                except psycopg2.Error as exc:
                    errors["values/" + key] = exc.pgcode

            groups = variables["profile_graph_models"]
            families = {g["model"]: sorted({f["metric"] for f in g["fields"]}) for g in groups}
            families.update(observation=variables["profile_graph_observations"],
                            non_value=list(variables["profile_graph_non_values"]))
            if set(types) != set().union(*map(set, families.values())):
                raise ValueError("catalog and installed bindings disagree")
            status_by_id = {}
            for group, ids in families.items():
                for metric in ids:
                    if metric in status_by_id:
                        continue
                    try:
                        # Equality pushes through both sides of the definitions LEFT JOIN.
                        # IN across different IDs can scan every other metric on a large copy.
                        source = cache if metric in cached_ids else view
                        cur.execute(f"select metric_id,value_status,count(*) from {source} "
                                    "where metric_id=%s group by 1,2 order by 2", (metric,))
                        status_by_id[metric] = cur.fetchall()
                        if not status_by_id[metric]:
                            errors["status/" + metric] = "missing_id"
                    except psycopg2.Error as exc:
                        errors["status/" + metric] = exc.pgcode
                status[group] = [row for metric in ids for row in status_by_id.get(metric, [])]
                checkpoint()
            for metric in variables["profile_graph_observations"]:
                compare(metric, observations(metric, args.schema, view))
                checkpoint()
            cur.execute("select exists(select from settings where key='sleep_duration_comparison_enabled' "
                        "and value='true')")
            sleep_enabled = cur.fetchone()[0]
            for group in groups:
                model = group["model"]
                if not sleep_enabled and model in ("sleep_duration_reference_v1", "semantic_sleep_duration_comparison_v1"):
                    continue
                if model in STORED.keys() | JSON_MODELS | {"semantic_sleep_duration_comparison_v1",
                        "daily_metrics", "archived_wake_time_v1", "archived_sleep_goal_v1", "archived_legacy_energy_eod_v1", "archived_energy_eod_v1"}:
                    for field, query in zip(group["fields"], archived_values(group, args.schema, view, types), strict=True):
                        compare(field["metric"], query)
            for metric, query in canonical_night_checks(args.schema, view):
                compare(metric, query)
            for metric, query in native_hrv_checks(args.schema, view):
                compare(metric, query)
            for metric, query in untyped_hrv_checks(cur, schema, view):
                compare(metric, query)
            for metric, query in oxygen_workout_checks(schema, view):
                compare(metric, query)
            for metric, query in shadow_night_checks(schema, view):
                compare(metric, query)
            for metric, counts in check_nights(cur, schema, cache if args.cache_night_reads else view):
                oracle[metric] = counts
                if len(set(counts[:3])) != 1 or counts[3]:
                    errors["values/" + metric] = "different"
            cur.execute("select metric,formula_version,count(*) from hdbt_decision_inputs group by 1,2 order by 1,2")
            print(json.dumps(dict(source_sha256=source_digest(), status=status, oracle=oracle,
                                  decision_journal=cur.fetchall(), errors=errors), sort_keys=True))
    finally:
        connection.close()
    return int(bool(errors))


if __name__ == "__main__":
    raise SystemExit(main())
