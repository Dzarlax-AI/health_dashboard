"""Load each synthetic profile into its own schema and query it as a read-only role."""
import json
import os
from pathlib import Path
import tempfile
import subprocess
import sys
import time

from profile_sql import profile_sql


def verify_dbt_installation(cid, sql):
    """Build in one existing profile with the rights of its local owner."""
    pilot = Path(__file__).resolve().parents[1]
    endpoint = subprocess.check_output(["docker", "port", cid, "5432/tcp"], text=True).strip()
    host, port = endpoint.rsplit(":", 1)
    if host != "127.0.0.1":
        raise RuntimeError("expected loopback PostgreSQL fixture")
    sql("""create role builder_0 login password 'synthetic-only';
        alter schema profile_0 owner to builder_0;
        alter table profile_0.metric_value_v1 owner to builder_0;
        alter table profile_0.metric_definition_v1 owner to builder_0;
        alter view profile_0.metric_read_v1 owner to builder_0;""")
    view = "profile_0.metric_read_v1"
    inspect = f"select pg_get_viewdef('{view}'::regclass)"
    expected = sql(inspect)
    # A prior reader exposed a smaller catalog. Make rollback observable even
    # for fixture profiles which have no measured steps.
    sql("set role builder_0; create or replace view profile_0.metric_read_v1 "
        "with (security_invoker=true) as select * from (" + expected.rstrip(";") +
        ") prior where metric_id <> 'daily.steps'")
    previous = sql(inspect)
    with tempfile.TemporaryDirectory(prefix="health-dbt-reader-") as tmp:
        config = {"health_dbt_metrics_pilot": {"target": "fixture", "outputs": {"fixture": {
            "type": "postgres", "host": host, "port": int(port), "user": "builder_0",
            "password": "synthetic-only", "dbname": "postgres", "schema": "profile_0", "threads": 1}}}}
        Path(tmp, "profiles.yml").write_text(json.dumps(config))
        subprocess.run([str(pilot/".venv/bin/dbt"), "run", "--project-dir", str(pilot),
                        "--profiles-dir", tmp, "--target-path", str(Path(tmp, "target")),
                        "--log-path", str(Path(tmp, "logs")), "--select", "metric_read_v1",
                        "--vars", json.dumps({"profile_metric_reads": True, "profile_metric_read_roles": ["reader_0"]})],
                       check=True, env=dict(os.environ, DBT_SEND_ANONYMOUS_USAGE_STATS="false"))
    current = sql(f"select pg_get_viewdef('{view}'::regclass)")
    access = sql(f"""select pg_get_userbyid(relowner)='builder_0'
                    and 'security_invoker=true'=any(reloptions)
                    and has_table_privilege('reader_0',oid,'select')
                    and not has_schema_privilege('builder_0','profile_1','usage')
                    and not has_table_privilege('reader_1',oid,'select')
                    from pg_class where oid='{view}'::regclass""")
    if current != expected or previous == current or access != "t":
        raise ValueError("dbt changed the profile reader definition or privileges")
    return previous


def main():
    bundle = json.load(sys.stdin)
    profiles = sorted({v["profile"] for v in bundle["values"]})
    if len(profiles) < 2:
        raise ValueError("need independent fixtures for schema isolation")
    definitions = bundle["definitions"]
    cid = subprocess.check_output(["docker","run","--rm","-d","-p","127.0.0.1::5432","--tmpfs","/var/lib/postgresql/data",
                                   "-e","POSTGRES_PASSWORD=synthetic-only","postgres:16-alpine"], text=True).strip()
    try:
        for _ in range(100):
            if subprocess.run(["docker","exec",cid,"pg_isready","-h","127.0.0.1","-U","postgres"],capture_output=True).returncode == 0:
                break
            time.sleep(.2)
        else:
            raise RuntimeError("fixture database failed to start")
        def sql(stmt):
            return subprocess.check_output(["docker","exec","-i",cid,"psql","-XqAt","-v","ON_ERROR_STOP=1","-U","postgres"],
                                           input=stmt,text=True).strip()
        # Definitions are common; values and user-facing query authority belong to one profile.
        for index, profile in enumerate(profiles):
            role, schema = f"reader_{index}", f"profile_{index}"
            sql(profile_sql(bundle, profile, schema))
            sql(f"create role {role}; grant usage on schema {schema} to {role}; "
                f"grant select on all tables in schema {schema} to {role};")
        previous = verify_dbt_installation(cid, sql)
        for index, profile in enumerate(profiles):
            peer = (index + 1) % len(profiles)
            result = sql(f"""set role reader_{index}; set search_path = profile_{index},pg_catalog;
                select jsonb_build_object('definitions',count(distinct metric_id),
                    'foreign_schema',has_schema_privilege(current_user,'profile_{peer}','usage'),
                    'samples',coalesce(jsonb_agg(jsonb_build_object('metric_id',metric_id,'source',source,
                    'sample_key',sample_key,'value',value,'value_type',definition->>'value_type',
                    'numeric_value',numeric_value,'text_value',text_value,'boolean_value',boolean_value,
                    'source_metadata',source_metadata,'value_status',value_status))
                    filter(where source is not null),'[]'::jsonb)) from metric_read_v1;""")
            got = json.loads(result)
            expected = [dict({k:v for k,v in r.items() if k != "profile"},
                             numeric_value=r["value"] if type(r["value"]) in (int, float) else None,
                             text_value=r["value"] if isinstance(r["value"], str) else None,
                             boolean_value=r["value"] if isinstance(r["value"], bool) else None,
                             value_status="missing" if r["value"] is None else "available")
                        for r in bundle["values"] if r["profile"] == profile]
            def identity(r):
                return r["metric_id"],r["source"],json.dumps(r["sample_key"],sort_keys=True)
            if (got["definitions"] != len(definitions) or got["foreign_schema"]
                    or sorted(got["samples"], key=identity) != sorted(expected,key=identity)):
                raise ValueError(f"{profile}: result or read privileges differ")
        count_as_reader = "set role reader_0; select count(*) from profile_0.metric_read_v1"
        before_rollback = int(sql(count_as_reader))
        steps_rows = int(sql(count_as_reader + " where metric_id='daily.steps'"))
        sql("set role builder_0; create or replace view profile_0.metric_read_v1 "
            "with (security_invoker=true) as " + previous.rstrip(";") + ";")
        if steps_rows < 1 or int(sql(count_as_reader)) != before_rollback - steps_rows or sql(
                "select pg_get_viewdef('profile_0.metric_read_v1'::regclass)") != previous:
            raise ValueError("rollback did not restore the previous reader")
        print("The dbt reader matched the standalone projection; owner, ACLs and rollback verified.")
        print(f"PostgreSQL returned the original values and keys for {len(profiles)} isolated fixture profiles.")
    finally:
        subprocess.run(["docker","rm","-fv",cid],check=True,stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
