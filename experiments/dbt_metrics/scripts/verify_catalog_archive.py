#!/usr/bin/env python3
"""Generate checks for a local archive restored into PostgreSQL.

The archive must contain the raw metric_points, the persisted input tables
used by the selected models, and the original sleep_duration_reference_v1
output. Create the metric_schema/schema.sql tables in the dbt target first.
Pass --emit vars to dbt --vars and --emit models to dbt --select. Once dbt
has built these models, execute --emit sql with psql -XAtq -v ON_ERROR_STOP=1.
--check FILE validates that every expected aggregate row was returned.
No command connects to production, writes data, or emits individual records.
"""

import argparse
import json
import re
from pathlib import Path

from check_semantic_catalog import definitions, read_catalog
from profile_graph_catalog import graph_catalog_vars

PILOT = Path(__file__).resolve().parents[1]
STORED = {
    "archived_main_night_v1": "completed_night_sleep",
    "archived_workout_v1": "workouts",
    "archived_energy_snapshot_v1": "energy_snapshots",
    "archived_target_v1": "target_snapshots",
    "archived_naive_baseline_v1": "naive_baselines",
    "archived_chip_calibration_v1": "chip_calibrations",
    "sleep_duration_reference_v1": "sleep_duration_reference_v1",
}
JSON_MODELS = {"archived_features_v1", "archived_today_evidence_v1", "archived_morning_sleep_v1"}
MODELS = sorted(STORED.keys() | JSON_MODELS | {"semantic_sleep_duration_comparison_v1"})
JSON_TYPES = {
    "number": "float8", "integer": "int", "boolean": "boolean", "text": "text",
    "json": "jsonb", "count_map": "jsonb", "timestamp": "timestamptz",
}


def literal(s):
    return "'" + s.replace("'", "''") + "'"


def ident(s):
    if not re.fullmatch("[a-z][a-z_0-9]{0,62}", s):
        raise ValueError(f"invalid identifier: {s!r}")
    return '"' + s + '"'


def archive_vars(schema, selected, with_observations):
    ident(schema)
    result = graph_catalog_vars()
    result.update(semantic_source_schema=schema, archive_records_schema=schema, archived_profile_fields=True,
                  semantic_shadow=True, sleep_duration_reference=True, profile_metric_reads=True)
    models = [group for group in result["profile_graph_models"] if group["model"] in selected]
    if sorted(group["model"] for group in models) != sorted(selected):
        raise ValueError("archive model bindings differ from the tested model list")
    result["profile_graph_models"] = models
    if not with_observations:
        result["profile_graph_observations"] = []
    return result


def reference_relation(group, source, types):
    model = group["model"]
    if model == "archived_main_night_v1":
        return f"""(select n.*,coalesce(n.diagnostics->>'preferred_sleep_source','') as preferred_sleep_source,
            coalesce(n.diagnostics->>'source_selection_reason','') as source_selection_reason
            from {ident(source)}.completed_night_sleep n)"""
    if model == "archived_morning_sleep_v1":
        columns = []
        for field in group["fields"]:
            col = field["column"]
            typ = JSON_TYPES[types[field["metric"]]]
            columns.append(f"(context->'sleep'->>{literal(col)})::{typ} as {ident(col)}")
        return f"""(select id,revision,local_date,language,format_revision,prepared_at,
            context->'sleep'->>'date' as sleep_date,{",".join(columns)}
            from {ident(source)}.report_history where kind='morning'
            and jsonb_typeof(context->'sleep')='object')"""
    if model in STORED:
        return f'{ident(source)}.{ident(STORED[model])}'
    if model == "semantic_sleep_duration_comparison_v1":
        macro = (PILOT / "macros/sleep_duration_comparison_fields.sql").read_text().strip()
        header, body = macro.split("\n", 1)
        if header != "{% macro sleep_duration_comparison_fields() %}" or not body.endswith("{% endmacro %}"):
            raise ValueError("unexpected sleep-comparison macro")
        return f'''(select n.wake_date,n.observed_at,r.reference_hours,status,duration_minutes,
            reference_minutes,delta_minutes,r.eligible_nights
            from {ident(source)}.completed_night_sleep n join {ident(source)}.sleep_duration_reference_v1 r
            using(wake_date,source,source_epoch,algorithm_version)
            {body.removesuffix("{% endmacro %}")})'''
    column_types = {field["column"]: JSON_TYPES[types[field["metric"]]] for field in group["fields"]}
    if model == "archived_today_evidence_v1":
        column_types.update(id="text", observed_at="timestamptz")
        projections = [f"(item->>{literal(k)})::{t} as {ident(k)}" for k, t in column_types.items()]
        return f'''(select date,lang,decision_id,schema_version,ordinality,{",".join(projections)}
            from {ident(source)}.daily_insight_bundles cross join lateral
            jsonb_array_elements(nullif(snapshot->'evidence','null'::jsonb)) with ordinality it(item,ordinality))'''
    projections = []
    for key, typ in column_types.items():
        if typ == "jsonb":
            value = f"nullif(features->{literal(key)},'null'::jsonb)"
        else:
            value = f"(features->>{literal(key)})::{typ}"
        projections.append(f"{value} as {ident(key)}")
    return f'''(select date,sub_score,source_epoch,feature_version,computed_at,{",".join(projections)}
        from {ident(source)}.feature_snapshots)'''


def archived_values(group, source, target, types):
    table = reference_relation(group, source, types)
    key_fields = [f'{literal(k)},a.{ident(k)}' for k in group["keys"]]
    for field in group["fields"]:
        key = key_fields.copy()
        col = field["column"]
        if field["component"]:
            key.append("'component'," + literal(col))
        filters = [f"a.{ident(k)}={literal(v)}" for k,v in field["filter"].items()]
        metadata = ["'column'," + literal(col)] + [
            f"{literal(k)},a.{ident(v)}" for k,v in group["metadata"].items()
        ]
        metric, model = literal(field["metric"]), literal(group["model"])
        yield f'''
with expected as (
  select jsonb_build_object({",".join(key)}) k, to_jsonb(a.{ident(col)}) v,
         jsonb_build_object({",".join(metadata)}) meta from {table} a where {" and ".join(filters) or "true"}
), candidate as (
  select sample_key k,value v,source_metadata meta from {target}
  where metric_id={metric} and source={model}
)
select json_build_array({metric},count(e.k),count(c.k),count(distinct c.k),
    count(*) filter(where e.k is null or c.k is null or e.v is distinct from c.v or e.meta is distinct from c.meta))
from expected e full join candidate c using(k);'''


def observations(metric, source, target):
    name = literal(metric)
    return f'''
with original as (
  select * from {ident(source)}.metric_points where metric_name={name}
), candidate as (
  select (sample_key->>'id')::bigint id,sample_key,source_metadata,original_observation_time,
         available_at,numeric_value,value_status from {target}
  where metric_id={name} and source={literal(source+".metric_points")}
)
select json_build_array({name},count(a.id),count(b.id),count(distinct b.id),
    count(*) filter(where a.id is null or b.id is null or b.numeric_value::real is distinct from a.qty
      or (b.value_status='missing') is distinct from (a.qty is null)
      or b.original_observation_time is distinct from a.date
      or b.available_at::timestamptz is distinct from a.received_at
      or b.source_metadata->>'quality' is distinct from a.quality
      or b.sample_key is distinct from jsonb_build_object('id',a.id,'original_date',a.date,
          'source',a.source,'units',a.units,'origin',a.origin,'health_record_id',a.health_record_id,
          'source_snapshot_at',a.source_snapshot_at)))
from original a full join candidate b using(id);'''


def check(path, variables):
    expected = set(variables["profile_graph_observations"]) | {
        f["metric"] for g in variables["profile_graph_models"] for f in g["fields"]
    }
    seen = set()
    populated = 0
    for line in path.read_text().splitlines():
        metric, old, new, unique, delta = json.loads(line)
        if (metric in seen or metric not in expected or
                any(type(n) is not int or n < 0 for n in (old,new,unique,delta))):
            raise ValueError("unexpected metric or invalid aggregate count")
        if old != new or new != unique or delta:
            raise ValueError(f"{metric}: archive mismatch {old}/{new}, unique={unique}, different={delta}")
        seen.add(metric)
        populated += old > 0
    if seen != expected:
        raise ValueError(f"{len(expected-seen)} expected comparisons absent")
    print(f"{len(seen)} IDs checked, {populated} with archived rows, {len(seen)-populated} empty")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    action = parser.add_mutually_exclusive_group(required=True)
    action.add_argument("--emit", choices=["vars","models","sql"])
    action.add_argument("--check", type=Path)
    parser.add_argument("--source-schema", default="source_data")
    parser.add_argument("--target-schema", default="measured")
    parser.add_argument("--models", nargs="+", choices=MODELS, default=MODELS)
    parser.add_argument("--without-observations", action="store_true")
    args = parser.parse_args()
    v = archive_vars(args.source_schema, args.models, not args.without_observations)
    if args.check:
        return check(args.check,v)
    if args.emit == "vars":
        print(json.dumps(v))
    elif args.emit == "models":
        print(" ".join(args.models + ["semantic_metric_catalog_v1","metric_read_v1"]))
    else:
        target = f"{ident(args.target_schema)}.metric_read_v1"
        types = {d["metric_id"]: d["value_type"] for d in definitions(read_catalog())}
        print("begin isolation level repeatable read read only; set local timezone='UTC'; set local jit=off;")
        for metric in v["profile_graph_observations"]:
            print(observations(metric,args.source_schema,target))
        for group in v["profile_graph_models"]:
            for stmt in archived_values(group,args.source_schema,target,types):
                print(stmt)
        print("commit;")


if __name__ == "__main__":
    main()
