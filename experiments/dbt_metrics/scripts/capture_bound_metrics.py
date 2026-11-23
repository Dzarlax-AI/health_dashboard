"""Read typed catalog bindings from their disposable PostgreSQL fixtures."""
import json
import os
from pathlib import Path
import sys

import psycopg2
from psycopg2 import sql
from check_semantic_catalog import definitions, read_catalog

# A single-profile schema still needs the complete key within that profile.
KEYS = read_catalog()["dbt_model_keys"]


PG_TYPES = {
    "number": {"smallint", "integer", "bigint", "numeric", "real", "double precision"},
    "text_list": {"text[]"},
    "number_map": {"jsonb"},
    "duration": {"bigint"},
    "integer": {"smallint", "integer", "bigint"},
    "text": {"text", "character varying"},
    "timestamp": {"timestamp with time zone", "timestamp without time zone"},
    "boolean": {"boolean"},
}


def capture(cur, metric_id, binding, kind, schema):
    model = binding["model"]
    columns = binding["columns"]
    keys = KEYS[model]
    filters = binding.get("filter", {})
    projection = sql.SQL(", ").join(map(sql.Identifier, keys + columns))
    predicate = sql.SQL(" and ").join(sql.SQL("{} = %s").format(sql.Identifier(k)) for k in filters)
    preferred = sql.SQL("({} is not null) desc").format(sql.Identifier(columns[0]))
    order = sql.SQL(", ").join([preferred, *map(sql.Identifier, keys)])
    query = sql.SQL("select {} from {} where {} order by {} limit 4").format(
        projection, sql.Identifier(schema, model), predicate if filters else sql.SQL("true"),
        order)
    # Let PostgreSQL encode timestamps, numeric values and SQL NULL in JSON.
    cur.execute(sql.SQL("select to_jsonb(sample) from ({}) sample").format(query), tuple(filters.values()))
    samples = [row for (row,) in cur.fetchall()]
    if not samples:
        raise ValueError(f"{metric_id}: bound model returned no fixture rows")
    cur.execute("select attname,format_type(atttypid,atttypmod) from pg_attribute "
                "where attrelid=%s::regclass and attname=any(%s) and attnum>0 and not attisdropped",
                (schema + "." + model, columns))
    types = dict(cur.fetchall())
    if set(types) != set(columns):
        raise ValueError(f"{metric_id}: missing typed column")
    if set(types.values()) - PG_TYPES[kind]:
        raise ValueError(f"{metric_id}: PostgreSQL columns {types} disagree with {kind}")
    results = []
    for row in samples:
        for column in columns:
            key = {k: row[k] for k in keys}
            if len(columns) > 1:
                key["component"] = column
            results.append(dict(metric_id=metric_id, profile=row.get("profile", "shadow-fixture"), key=key,
                                source=model, column=column, sql_type=types[column], value=row[column]))
    return results


def main():
    catalog = read_catalog()
    kinds = {item["metric_id"]:item["value_type"] for item in definitions(catalog)}
    bindings = catalog["dbt_bindings"]
    lane = sys.argv[2] if len(sys.argv) > 2 else "semantic"
    selected_models = set(sys.argv[3:])
    if selected_models - KEYS.keys():
        raise ValueError("unknown capture model")
    user, database, schema = {
        "semantic": ("shadow_reader", "sem_synthetic", "dbt_output"),
        "energy": ("pilot_dbt", "storage_pilot", "dbt_output"),
        "baseline": ("postgres", "personal_baseline_fixture", "derived"),
        "briefing": ("postgres", "postgres", "derived"),
        "aggregates": ("pilot_dbt", "hp_dbt_pilot", "dbt_output"),
        "readiness": ("pilot_dbt", "readiness_hrv_shadow", "dbt_output"),
        "workouts": ("pilot_dbt", "storage_pilot", "dbt_output"),
    }[lane]
    with psycopg2.connect(host="127.0.0.1", port=int(sys.argv[1]), user=user,
                          password="synthetic-only", dbname=database) as conn, conn.cursor() as cur:
        if lane == "semantic":
            cur.execute("select token from public.sleep_fixture_identity")
            if cur.fetchone() != (os.environ["SLEEP_DOCKER_TOKEN"],):
                raise ValueError("unexpected database identity")
        rows = [row for key, binding in bindings.items() if binding.get("lane", "semantic") == lane
                if not selected_models or binding["model"] in selected_models
                for row in capture(cur, key, binding, kinds[key], schema)]
    (Path(os.environ["HEALTH_METRIC_AUDIT_DIR"]) / f"dbt-{lane}.json").write_text(json.dumps(rows))


if __name__ == "__main__":
    main()
