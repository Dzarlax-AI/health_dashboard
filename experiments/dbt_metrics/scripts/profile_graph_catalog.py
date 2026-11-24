"""Compile the catalog-declared bindings into one profile graph reader."""
from collections import defaultdict
import json
import re

from check_semantic_catalog import definitions, read_catalog


def graph_catalog_vars():
    catalog = read_catalog()
    bindings = defaultdict(list)
    for metric, binding in catalog["dbt_bindings"].items():
        for col in binding["columns"]:
            bindings[binding["model"]].append({
                "metric": metric, "column": col, "component": len(binding["columns"]) > 1,
                "filter": binding.get("filter", {}),
            })
    keys = catalog["dbt_model_keys"]
    if set(keys) != set(bindings):
        raise ValueError("model keys differ from the catalog bindings")
    return {"profile_graph_observations": sorted(catalog["observations"]),
            "profile_graph_non_values": catalog["non_values"], "profile_graph_models": [
        {"model": model, "keys": keys[model], "fields": fields,
         "metadata": catalog["dbt_model_metadata"].get(model, {})}
        for model, fields in sorted(bindings.items())
    ]}


def verify_graph_catalog(sql, graph_schema, source_schema):
    if not all(re.fullmatch("[a-z_][a-z0-9_]*", name) for name in (graph_schema, source_schema)):
        raise ValueError("unexpected profile graph schema")
    catalog = read_catalog()
    expected_ids = {d["metric_id"] for d in definitions(catalog)}
    view = f'"{graph_schema}".metric_read_v1'
    # Exercise the observation transport for every catalogued ingress ID.
    # Roll back the probes before measuring the fixture's actual coverage.
    observations = sorted(set(catalog["observations"]) - catalog["non_values"].keys())
    encoded = json.dumps(observations).replace("'", "''")
    observed = json.loads(sql(f"""
        begin;
        insert into "{source_schema}".metric_points(metric_name,date,source,qty,quality,units)
        select metric,'2024-09-01 12:00:00 +0000','catalog-observation-probe',0,'ok','fixture'
        from jsonb_array_elements_text('{encoded}'::jsonb) metric;
        select jsonb_agg(metric_id order by metric_id) from {view}
        where sample_key->>'source'='catalog-observation-probe'
          and source='{source_schema}.metric_points' and numeric_value=0
          and value_status='available' and sample_key->>'units'='fixture';
        rollback;
    """))
    if sorted(observed or []) != observations:
        raise ValueError("catalog observation reader lost a measured zero, its source or its units")
    print(f"PASS: {len(observations)} native observations retain measured zero and provenance.", flush=True)
    # Compare every date, NULL and measured value for one bound field. This
    # includes rows after the four examples used by the separate fixture audit.
    result = json.loads(sql(f"""
        with native as (select date, steps::numeric as value from "{graph_schema}".daily_metrics),
        projected as (select sample_key->>'date' as date, numeric_value as value
            from {view} where metric_id='daily.steps' and source='daily_metrics'),
        delta as ((select * from native except all select * from projected)
            union all (select * from projected except all select * from native))
        select json_build_object('delta',(select count(*) from delta),
            'dates',(select count(*) from native),
            'missing',(select count(*) from projected where value is null))
    """))
    if result["delta"] or result["dates"] <= 4 or not result["missing"]:
        raise ValueError(f"daily graph projection mismatch: {result}")
    rows = json.loads(sql(f"""
        select json_agg(row_to_json(t)) from (
        select metric_id, count(source) as samples,
            count(distinct (source,sample_key)) filter (where source is not null) as unique_keys,
            count(*) filter (where value_status in ('input_contract','dormant','unclassified')
                                and reason is null) as missing_reason
        from {view} group by metric_id) t"""))
    actual = {row["metric_id"] for row in rows}
    if actual != expected_ids:
        raise ValueError("installed metric catalog differs from project definitions")
    for row in rows:
        if row["samples"] != row["unique_keys"] or row["missing_reason"]:
            raise ValueError(f"invalid graph sample identity or status: {row}")
    missing = catalog["dbt_bindings"].keys() - {row["metric_id"] for row in rows if row["samples"]}
    if missing:
        raise ValueError(f"catalog bindings with no profile graph samples: {sorted(missing)}")
    projected_count = verify_bound_values(sql, graph_schema)
    verify_observations_and_capture(sql, graph_schema, source_schema, catalog)
    # Mirror NewLocalProfileGraphReader's connection setting even when a
    # caller supplies its own SQL runner with PostgreSQL defaults.
    plan = json.loads(sql(f"""set jit=off; explain (analyze, timing off, settings, format json)
        select numeric_value from {view} where metric_id='daily.steps'
        and sample_key->>'date'='2026-02-01'"""))[0]
    if plan.get("Settings", {}).get("jit") != "off" or "JIT" in plan:
        raise ValueError("dated catalog probe did not use the reader JIT configuration")
    if plan["Plan"]["Actual Rows"] != 1:
        raise ValueError("expected one dated daily.steps measurement")
    nodes = [plan["Plan"]]
    while nodes:
        node = nodes.pop()
        if (node["Actual Loops"] and "Relation Name" in node and node["Relation Name"] not in
                {"daily_metrics", "metric_value_v1", "metric_definition_v1"}):
            raise ValueError("dated steps lookup scanned an unrelated relation: " + node["Relation Name"])
        nodes.extend(node.get("Plans", []))
    populated = sum(row["samples"] > 0 for row in rows)
    print(f"PASS: {len(actual)} catalog IDs, {len(catalog['dbt_bindings'])} graph bindings; "
          f"{projected_count} modeled samples match their typed producer rows; "
          f"{populated} IDs have samples in this combined fixture. "
          f"All {result['dates']} graph step values matched, including {result['missing']} NULLs. "
          f"Dated lookup: {plan['Planning Time']} ms planning, {plan['Execution Time']} ms execution.", flush=True)


def verify_bound_values(sql, graph):
    """Independently unpivot every producer and compare the full result multiset."""
    groups = graph_catalog_vars()["profile_graph_models"]
    branches = []
    for group in groups:
        # Inputs are local checked-in catalog identifiers. Keep the SQL relation
        # quoted and pass all catalog fields as a JSON literal.
        model = group["model"]
        if not re.fullmatch("[a-z_][a-z0-9_]*", model):
            raise ValueError("invalid producer model identifier")
        spec = json.dumps(group).replace("'", "''")
        branches.append(f"""
            select field->>'metric' as metric_id,'{model}' as source,
                   k.identity || case when (field->>'component')::boolean
                      then jsonb_build_object('component',field->>'column') else '{{}}'::jsonb end as sample_key,
                   nullif(r.payload->(field->>'column'),'null'::jsonb) as value,
                   jsonb_build_object('column',field->>'column') || m.attrs as source_metadata
            from (select to_jsonb(q) as payload from "{graph}"."{model}" q) r
            cross join (select '{spec}'::jsonb as spec) g
            cross join lateral jsonb_array_elements(spec->'fields') f(field)
            cross join lateral (select jsonb_object_agg(key,r.payload->key) as identity
                from jsonb_array_elements_text(spec->'keys') keys(key)) k
            cross join lateral (select coalesce(jsonb_object_agg(key,r.payload->col),'{{}}') as attrs
                from jsonb_each_text(spec->'metadata') attrs(key,col)) m
            where not exists (select 1 from jsonb_each_text(field->'filter') conditions(key,v)
                              where r.payload->>key is distinct from v)
        """)
    # One transaction pins the generation. Read the installed view once, then
    # compare one native relation at a time to bound independent source plans.
    statements = [f"""
        begin;
        set local jit=off;
        create temporary table checked_projection on commit drop as
            select metric_id,source,sample_key,value,source_metadata from "{graph}".metric_read_v1
            where source is not null;
        create index on checked_projection(source);
        analyze checked_projection;
    """]
    for group, expected in zip(groups, branches, strict=True):
        name = group["model"]
        statements.append(f"""
            with expected as materialized ({expected}),
            projected as (select * from checked_projection where source='{name}'),
            differences as ((select * from expected except all select * from projected)
                  union all (select * from projected except all select * from expected))
            select json_build_object('model','{name}','count',(select count(*) from expected),
                'diff',(select count(*) from differences));
        """)
    statements.append("rollback;")
    results = [json.loads(line) for line in sql("\n".join(statements)).splitlines()]
    if len(results) != len(groups):
        raise ValueError("incomplete typed catalog projection check")
    for result in results:
        if result["diff"] or not result["count"]:
            raise ValueError(f"typed catalog projection differs from producers: {result}")
    return sum(result["count"] for result in results)


def verify_observations_and_capture(sql, graph, source, catalog):
    """Exercise every raw ID and an unbound Go capture without changing subsequent test inputs."""
    raw_ids = json.dumps(sorted(catalog["observations"])).replace("'", "''")
    observed = "2030-10-27 02:00:00 +0200"
    receipt = "2030-10-27 01:15:00+00"
    captured = "today_evidence.domain"
    direct = f"{source}.metric_points"
    row = json.loads(sql(f"""
        begin;
        insert into {source}.metric_points(metric_name,date,source,qty,units,quality,origin,received_at)
        select id,'{observed}','graph-audit',case n%3 when 0 then null when 1 then 0 else n end,
               'fixture','ok','live','{receipt}'::timestamptz
        from jsonb_array_elements_text('{raw_ids}'::jsonb) with ordinality ids(id,n);
        insert into {graph}.metric_definition_v1(metric_id,definition,disposition,read_sources)
        select metric_id,definition,'active','["recorded_decision"]'::jsonb
        from {graph}.semantic_metric_catalog_v1 where metric_id='{captured}';
        insert into {graph}.metric_value_v1(metric_id,value_type,source,sample_key,value,source_metadata)
        values('{captured}','text','recorded_decision','{{"capture":1}}','"sleep"',
            '{{"observed_at":"original","available_at":"receipt","version":"v1"}}');
        with want as (
            select metric_name,id,date,source,units,origin,qty,received_at,quality
            from {source}.metric_points where source='graph-audit'
        ), actual as (
            select metric_id,(sample_key->>'id')::bigint as id,original_observation_time,
                   sample_key->>'source',sample_key->>'units',sample_key->>'origin',
                   numeric_value::real,available_at::timestamptz,source_metadata->>'quality'
            from {graph}.metric_read_v1 where source='{direct}' and sample_key->>'source'='graph-audit'
        ), diff as (
            (select * from actual except all select * from want)
            union all (select * from want except all select * from actual)
        )
        select json_build_object('observations',(select count(*) from want),
            'differences',(select count(*) from diff),'captures',(
                select count(*) from {graph}.metric_read_v1 where metric_id='{captured}'
                and source='recorded_decision' and text_value='sleep' and original_observation_time='original'
                and available_at='receipt' and source_version='v1' and value_status='available'
                and sample_key='{{"capture":1}}'::jsonb));
        rollback;
    """))
    if row != {"observations": len(catalog["observations"]), "differences": 0, "captures": 1}:
        raise ValueError(f"raw observations or recorded input snapshot differ: {row}")
