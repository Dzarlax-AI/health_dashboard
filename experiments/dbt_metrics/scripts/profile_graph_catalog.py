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
    return {"profile_graph_non_values": catalog["non_values"], "profile_graph_models": [
        {"model": model, "keys": keys[model], "fields": fields}
        for model, fields in sorted(bindings.items())
    ]}


def verify_graph_catalog(sql, graph_schema):
    if not re.fullmatch("[a-z_][a-z0-9_]*", graph_schema):
        raise ValueError("unexpected profile graph schema")
    catalog = read_catalog()
    expected_ids = {d["metric_id"] for d in definitions(catalog)}
    view = f'"{graph_schema}".metric_read_v1'
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
    plan = json.loads(sql(f"""explain (analyze, timing off, format json)
        select numeric_value from {view} where metric_id='daily.steps'
        and sample_key->>'date'='2026-02-01'"""))[0]
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
          f"{populated} IDs have samples in this combined fixture. "
          f"All {result['dates']} graph step values matched, including {result['missing']} NULLs. "
          f"Dated lookup: {plan['Planning Time']} ms planning, {plan['Execution Time']} ms execution.", flush=True)
