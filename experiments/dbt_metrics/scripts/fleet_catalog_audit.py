"""Generate a read-only per-tenant SQL audit and combine its aggregate-only results.

Run --sql inside a tenant transaction with its verified search path. The
caller owns authentication. Use one REPEATABLE READ READ ONLY snapshot per
profile. Combine the resulting JSON objects with --reports profile1 ... .
"""
import argparse
from collections import Counter
import json
from pathlib import Path
import re

from check_semantic_catalog import definitions, read_catalog


def audit_sql(catalog, formula="aggregate_serving_v3"):
    if formula not in ("aggregate_serving_v2", "aggregate_serving_v3"):
        raise ValueError("unsupported aggregate publication formula")
    daily = []
    for metric, binding in sorted(catalog["dbt_bindings"].items()):
        if binding["model"] != "daily_metrics":
            continue
        col, = binding["columns"]
        if not re.fullmatch("[a-z][a-z0-9_]*", col) or metric != "daily." + col:
            raise ValueError(f"unexpected daily binding: {metric}")
        daily.append(f"'{metric}', jsonb_build_object("
                     f"'reference',count(a.{col}), 'candidate',count(b.{col}), "
                     f"'missing',count(*) filter(where a.{col} is not null and b.{col} is null), "
                     f"'extra',count(*) filter(where a.{col} is null and b.{col} is not null), "
                     f"'different',count(*) filter(where a.{col} <> b.{col}), "
                     f"'max_delta',coalesce(max(abs(a.{col}::float8-b.{col}::float8)),0))")
    return """
WITH hours AS (
 SELECT coalesce(a.metric_name,b.metric_name) AS metric,
        count(a.hour) AS reference, count(b.hour) AS candidate,
        count(*) FILTER (WHERE b.hour IS NULL) AS missing,
        count(*) FILTER (WHERE a.hour IS NULL) AS extra,
        count(*) FILTER (WHERE a.hour IS NOT NULL AND b.hour IS NOT NULL AND
            (a.avg_val,a.min_val,a.max_val,a.sample_count) IS DISTINCT FROM
            (b.avg_val,b.min_val,b.max_val,b.sample_count)) AS different
 FROM hourly_metrics a FULL JOIN hdbt_hourly_metrics b USING(metric_name,hour,source)
 GROUP BY 1
), days AS (
 SELECT jsonb_build_object(""" + ",\n".join(daily) + """) AS metrics,
        count(a.date) AS reference_dates,count(b.date) AS candidate_dates,
        count(*) FILTER(WHERE a.date IS NULL) AS extra_dates,
        count(*) FILTER(WHERE b.date IS NULL) AS missing_dates
 FROM daily_scores a FULL JOIN hdbt_daily_metrics b USING(date)
)
SELECT jsonb_build_object('ready',
 coalesce(s.shadow_enabled AND s.initialized AND s.ready AND s.formula_id='""" + formula + """'
 AND NOT EXISTS(SELECT FROM hdbt_backfill) AND NOT EXISTS(SELECT FROM hdbt_dirty)
 AND s.source_generation=(SELECT (value::jsonb->>'generation')::numeric
                           FROM settings WHERE key='_cache_maintenance_v1'),false),
 'formula',s.formula_id,'generation',s.source_generation,'daily',d.metrics,'reference_dates',d.reference_dates,
 'candidate_dates',d.candidate_dates,'extra_dates',d.extra_dates,'missing_dates',d.missing_dates,
 'hourly',coalesce((SELECT jsonb_object_agg(metric,to_jsonb(hours)-'metric') FROM hours),'{}'))
FROM days d CROSS JOIN hdbt_state s WHERE singleton
"""


def verdict(row):
    if row["missing"] or row["extra"] or row["different"]:
        return "different"
    return "exact" if row["reference"] else "no_samples"


def combine(catalog, reports):
    if not reports or not all(r["ready"] for r in reports):
        raise ValueError("each input must be a completed and current publication")
    metrics = {}
    for definition in definitions(catalog):
        metric = definition["metric_id"]
        binding = catalog["dbt_bindings"].get(metric)
        evidence = []
        for r in reports:
            if binding and binding["model"] == "daily_metrics":
                evidence.append(verdict(r["daily"][metric]))
            elif metric in catalog["non_values"]:
                evidence.append(catalog["non_values"][metric][0])
            else:
                evidence.append("requires_archive_graph")
        metrics[metric] = {"daily_publication": evidence}
        if metric in catalog["observations"]:
            metrics[metric]["hourly_projection"] = [
                verdict(r["hourly"][metric]) if metric in r["hourly"] else "no_samples"
                for r in reports]
    return {"definitions": len(metrics), "profiles": reports, "metrics": metrics,
            "daily_outcomes": dict(Counter(s for m in metrics.values() for s in m["daily_publication"]))}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    opts = parser.add_mutually_exclusive_group(required=True)
    opts.add_argument("--sql", action="store_true")
    opts.add_argument("--reports", type=Path, nargs="+")
    parser.add_argument("--formula", choices=("aggregate_serving_v2", "aggregate_serving_v3"),
                        default="aggregate_serving_v3", help="required formula for the publication being audited")
    args = parser.parse_args()
    catalog = read_catalog()
    if args.sql:
        print(audit_sql(catalog, args.formula))
    else:
        print(json.dumps(combine(catalog, [json.loads(p.read_text()) for p in args.reports]), indent=2))
