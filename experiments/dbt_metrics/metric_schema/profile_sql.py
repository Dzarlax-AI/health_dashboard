"""Render one bundle profile as SQL for a new local PostgreSQL schema."""
import argparse
import json
from pathlib import Path

TABLES = Path(__file__).with_name("schema.sql").read_text()


def metric_view_sql():
    macro = (Path(__file__).resolve().parents[1]/"macros/profile_metric_rows.sql").read_text()
    header, body = macro.strip().split("\n", 1)
    if header != "{% macro profile_metric_rows(definitions, facts) %}" or not body.endswith("{% endmacro %}"):
        raise ValueError("invalid metric reader macro")
    query = body.removesuffix("{% endmacro %}").replace("{{ definitions }}", "metric_definition_v1").replace(
        "{{ facts }}", "metric_value_v1")
    return "create or replace view metric_read_v1 with (security_invoker=true) as " + query.strip() + ";"



def literal(value):
    return "'" + value.replace("'", "''") + "'"


def identifier(value):
    if not value or "\0" in value or len(value.encode()) > 63:
        raise ValueError("invalid PostgreSQL schema name")
    return '"' + value.replace('"', '""') + '"'


def profile_sql(bundle, profile, schema_name):
    definitions = bundle["definitions"]
    rows = [{k:v for k,v in row.items() if k != "profile"} for row in bundle["values"] if row["profile"] == profile]
    if not rows or bundle["schema_version"] != 1:
        raise ValueError("unknown profile or unsupported bundle version")
    schema = identifier(schema_name)
    return f"""begin;
set local standard_conforming_strings = on;
create schema {schema};
set local search_path = {schema}, pg_catalog;
{TABLES}
{metric_view_sql()}
insert into metric_definition_v1(metric_id,definition,disposition,reason,read_sources,dbt_binding)
select metric_id,definition,disposition,reason,read_sources,dbt_binding
from jsonb_populate_recordset(null::metric_definition_v1,{literal(json.dumps(definitions))}::jsonb);
insert into metric_value_v1 select * from jsonb_populate_recordset(
null::metric_value_v1,{literal(json.dumps(rows))}::jsonb);
commit;"""


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    source = parser.add_mutually_exclusive_group()
    source.add_argument("--persisted", type=Path, help="local reader records exported by observations/persisted.py")
    source.add_argument("--bundle", type=Path, default=Path(__file__).resolve().parents[1]/"artifacts/unified-metrics.json")
    parser.add_argument("--profile", required=True)
    parser.add_argument("--schema", required=True)
    args = parser.parse_args()
    if args.persisted:
        from run import assemble
        bundle = assemble([args.persisted], synthetic=False)
    else:
        bundle = json.loads(args.bundle.read_text())
    print(profile_sql(bundle, args.profile, args.schema))
