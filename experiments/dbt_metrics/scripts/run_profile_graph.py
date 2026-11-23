#!/usr/bin/env python3
"""Build the complete dbt model graph as a restricted role in a disposable PostgreSQL fixture."""
import argparse
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time
from urllib.parse import urlencode

from profile_graph_catalog import graph_catalog_vars, verify_graph_catalog
from readiness_score_cases import prepare, score_cases
from run_briefing_components import fixtures as briefing_rows
from run_pilot import IMAGE
from run_readiness_hrv_shadow import snapshot_fixture_sql

PILOT = Path(__file__).resolve().parents[1]
DB = "health_profile_graph_fixture"
FEATURES = (
    "semantic_shadow", "sleep_daily_shadow", "sleep_duration_reference",
    "semantic_sleep_cache", "semantic_night_card_cache", "readiness_hrv_shadow",
    "readiness_score_shadow", "readiness_chips_shadow", "energy_bank_shadow",
    "workout_shadow", "briefing_components_shadow", "complex_sleep_parity",
    "personal_baseline_shadow", "profile_metric_reads", "archived_profile_fields",
    "verified_step_withdrawals", "decision_journal_replay",
)
# Inputs use the same fixture contracts as the individual parity lanes. Names
# differ from the defaults so an accidentally hardcoded reference fails.
SOURCES = {
    "archive_records_schema": ("archive_records", "profile_archived"),
    "semantic_source_schema": ("source_data", "profile_source"),
    "briefing_source_schema": ("briefing_pilot", "profile_briefing"),
    "energy_source_schema": ("energy_capture", "profile_energy"),
    "sleep_shadow_source_schema": ("sleep_shadow", "profile_candidates"),
    "workout_source_schema": ("workout_capture", "profile_workouts"),
    "personal_baseline_source_schema": ("baseline_fixture", "profile_baselines"),
}


def prepare_inputs(sql, marker):
    def load(name):
        sql((PILOT / name).read_text())

    load("semantic/inputs.sql")
    load("semantic/synthetic.sql")
    sql("alter schema source_data rename to night_fixture")
    load("fixtures/schema.sql")
    load("fixtures/base.sql")
    load("fixtures/daily_average_sources.sql")
    sql("insert into source_data.calendar select * from night_fixture.calendar")
    for relation in ("completed_night_sleep", "sleep_interval", "sleep_stage_day",
                     "hrv_point", "workout", "daily_hrv"):
        sql(f"alter table night_fixture.{relation} set schema source_data")
    sql("create role shadow_dbt; create role shadow_reader")
    for name in ("metric_schema/shadow_inputs.sql", "semantic/complex_sleep_fixture.sql",
                 "fixtures/readiness_chips.sql", "fixtures/energy_capture.sql",
                 "fixtures/workout_capture.sql", "fixtures/personal_baseline.sql",
                 "fixtures/briefing_capture.sql",):
        load(name)
    load("fixtures/profile_archive_records.sql")
    load("fixtures/profile_graph_cases.sql")
    load("fixtures/profile_step_aliases.sql")
    load("fixtures/profile_sections_points.sql")
    load("fixtures/profile_screen_sleep_sources.sql")
    sql(snapshot_fixture_sql())
    prepare(lambda _, text: sql(text), None, score_cases())
    rows = briefing_rows()
    sql("copy briefing_pilot.captured_inputs from stdin;\n" + "".join(
        f"graph-fixture\t{i}\t{json.dumps(row)}\n" for i, row in enumerate(rows)) + "\\.\n")
    sql("create role peer_builder login password 'synthetic-only'; create schema peer_graph authorization peer_builder; "
        "alter schema dbt_output rename to profile_graph; "
        "create table source_data.profile_graph_fixture_marker(token text primary key); "
        f"insert into source_data.profile_graph_fixture_marker values ('{marker}'); "
        "alter table source_data.completed_night_sleep add diagnostics jsonb default '{}'::jsonb; "
        "create table source_data.settings(key text primary key,value text,updated_at text);")
    for old, new in SOURCES.values():
        sql(f"alter schema {old} rename to {new}; alter schema {new} owner to pilot_dbt; "
            f"grant select on all tables in schema {new} to pilot_dbt")
    sql("set search_path=profile_source;" + (PILOT.parents[1] / "internal/storage/sql/hdbt_decision_inputs.sql").read_text())
    # This local role also serves as the tenant login for the read/rollback probe.
    sql(r"""select format('alter table %I.%I owner to pilot_dbt',schemaname,tablename)
        from pg_tables where schemaname='profile_source' \gexec""")
    sql("grant insert on profile_energy.runs,profile_energy.days to pilot_dbt")
    # Copy only tables, never views that could lead back into the first profile.
    for original in [schema for _, schema in SOURCES.values()]:
        peer = original.replace("profile_", "peer_", 1)
        sql(f"create schema {peer} authorization peer_builder")
        sql(rf"""select format('create table {peer}.%I (like %I.%I including constraints including indexes);
                   insert into {peer}.%I select * from %I.%I;
                   alter table {peer}.%I owner to peer_builder',
                   tablename,schemaname,tablename,tablename,schemaname,tablename,tablename)
            from pg_tables where schemaname='{original}' \gexec""")
    # LIKE omits defaults. A separate sequence keeps later peer observations
    # independent of the primary profile, including their tie-breaking IDs.
    sql("""set role peer_builder;
        create sequence peer_source.metric_points_id_seq owned by peer_source.metric_points.id;
        select setval('peer_source.metric_points_id_seq', coalesce(max(id),1), max(id) is not null)
          from peer_source.metric_points;
        alter table peer_source.metric_points
          alter column id set default nextval('peer_source.metric_points_id_seq'),
          alter column origin set default 'live', alter column received_at set default now(),
          alter column quality set default 'ok'""")
    sql("update peer_source.metric_points set qty=71 where metric_name='step_count' "
        "and date='2026-02-01 09:00:00 +0000'")
    for name, role in (("profile", "pilot_dbt"), ("peer", "peer_builder")):
        sql(f"set role {role}; set search_path={name}_graph,pg_catalog; " +
            (PILOT / "metric_schema/schema.sql").read_text())


def check_build(lab, sql, graph, role):
    nodes = json.loads((lab / "target/manifest.json").read_text())["nodes"]
    results = json.loads((lab / "target/run_results.json").read_text())["results"]
    models = {n["name"]: n for n in nodes.values() if n["resource_type"] == "model"}
    expected = {f.stem for f in (PILOT / "models").rglob("*.sql")}
    # This fixture supplies normalized HRV points. Its alternate source adapter
    # is built from real HTTP and XML receipts in TestTypedHRVReceiptIngest.
    expected.remove("hrv_receipt_points_v1")
    if models.keys() != expected:
        raise ValueError(f"manifest differs from model sources: {sorted(expected ^ models.keys())}")
    physical = {n["alias"] for n in models.values() if n["config"]["materialized"] != "ephemeral"}
    tested = {r["unique_id"] for r in results if r["status"] == "pass"}
    built = {r["unique_id"] for r in results if r["status"] == "success"}
    if ({n["unique_id"] for n in models.values() if n["alias"] in physical} != built or
            {k for k,n in nodes.items() if n["resource_type"] == "test"} != tested or not tested):
        raise ValueError("dbt did not successfully execute the complete graph and test set")
    installed = sql(f"""select relname from pg_class c join pg_namespace n on c.relnamespace=n.oid
        where n.nspname='{graph}' and relkind in ('r','v','m','p')
        and pg_get_userbyid(relowner)='{role}'""").splitlines()
    if set(installed) != physical | {"metric_value_v1", "metric_definition_v1"}:
        raise ValueError("physical relations differ from the graph manifest")
    # Each builder gets its own input and result schemas. This includes every
    # auxiliary capture schema used by models.
    forbidden_prefix = "peer_" if role == "pilot_dbt" else "profile_"
    exposed = sql(f"""select nspname from pg_namespace
        where starts_with(nspname,'{forbidden_prefix}') and has_schema_privilege('{role}',oid,'usage')""")
    if exposed:
        raise ValueError(f"{role} can access foreign schemas: {exposed}")
    print(f"PASS: {len(models)} source models, {len(physical)} owned relations, "
          f"{len(tested)} dbt tests; peer schema isolated.", flush=True)


def build(lab, name, *options, affected_dates=None, expect_failure=False):
    variables = dict.fromkeys(FEATURES, True)
    variables.update(graph_catalog_vars())
    variables.update({key: schema.replace("profile_", name + "_", 1)
                      for key, (_, schema) in SOURCES.items()})
    if affected_dates is not None:
        variables["affected_dates"] = affected_dates
    result = subprocess.run(
        [PILOT / ".venv/bin/dbt", "build", "--profiles-dir", lab, "--project-dir", PILOT,
         "--target-path", lab / "target", "--log-path", lab / "logs", *options, "--vars", json.dumps(variables)],
        check=not expect_failure, capture_output=expect_failure, text=True,
        env={**os.environ, "DBT_SEND_ANONYMOUS_USAGE_STATS": "false"})
    if expect_failure and (result.returncode == 0 or
            "affected_dates must exist in the profile calendar" not in result.stdout + result.stderr):
        raise ValueError("expected rejection of a date outside this profile calendar")


def install_and_probe(lab, name, role, peer_graph, sql, host, port, token, probe, capture_archive=False):
    graph, source = f"{name}_graph", f"{name}_source"
    lab.mkdir()
    config = {"type": "postgres", "host": host, "port": int(port), "dbname": DB,
              "user": role, "password": "synthetic-only", "schema": graph, "threads": 2,
              "search_path": "pg_catalog"}
    (lab / "profiles.yml").write_text(json.dumps({"health_dbt_metrics_pilot": {
        "target": "local", "outputs": {"local": config}}}))
    load_profile_fixture(sql, source, role, "profile_screen_metrics.sql")
    build(lab, name, "--full-refresh")
    if capture_archive:
        subprocess.run([PILOT/".venv/bin/python", PILOT/"scripts/capture_bound_metrics.py",
                        str(port), "archive"], check=True)
        return
    check_build(lab, sql, graph, role)
    verify_graph_catalog(lambda query: sql(f"set role {role}; " + query), graph, source)
    for filename in ("profile_reader_legacy.sql", "profile_sections_legacy.sql"):
        load_profile_fixture(sql, source, role, filename)
    dsn = f"postgresql://{role}:synthetic-only@{host}:{port}/{DB}?" + urlencode({
        "sslmode": "disable", "search_path": source})
    env = dict(os.environ, HEALTH_PROFILE_GRAPH_PROBE="1", DATABASE_URL=dsn, PROFILE_GRAPH_FIXTURE_TOKEN=token, PROFILE_ENERGY_SCHEMA=name+"_energy",
               PROFILE_SOURCE_SCHEMA=source, PROFILE_GRAPH_SCHEMA=graph, PROFILE_PEER_SCHEMA=peer_graph)
    subprocess.run([probe.resolve()], check=True, env=env)
    return env


def load_profile_fixture(sql, source, role, filename):
    sql(f"set role {role}; set search_path={source},pg_catalog; " +
        (PILOT / "fixtures" / filename).read_text())


def screen_probes(probe, environments, phase, suite):
    for env in environments:
        subprocess.run([probe.resolve()], check=True, env=dict(env, PROFILE_SCREEN_PHASE=phase, PROFILE_SCREEN_SUITE=suite))



def check_incremental(lab, sql):
    def day_value(schema, role):
        return sql(f"set role {role}; select numeric_value from {schema}.metric_read_v1 "
                   "where metric_id='daily.steps' and source='daily_metrics' "
                   "and sample_key->>'date'='2026-02-01'")

    def unaffected_rows():
        # Compare all other primary dates and all peer dates exactly.
        return sql("""select jsonb_build_array(
            (select jsonb_agg(to_jsonb(p) order by date) from profile_graph.daily_metrics p
             where date <> '2026-02-01'),
            (select jsonb_agg(to_jsonb(p) order by date) from peer_graph.daily_metrics p))""")

    if (day_value("profile_graph", "pilot_dbt"), day_value("peer_graph", "peer_builder")) != ("17", "71"):
        raise ValueError("distinct profile source points did not reach their own graph")
    before = unaffected_rows()
    sql("set role pilot_dbt; update profile_source.metric_points set qty=19 "
        "where metric_name='step_count' and date='2026-02-01 09:00:00 +0000'")
    load_profile_fixture(sql, "profile_source", "pilot_dbt", "profile_screen_sleep_correction.sql")
    build(lab, "profile", "--select", "+daily_metrics", affected_dates=["2026-02-01"])
    build(lab, "profile", "--select", "+daily_metrics", affected_dates=["1900-01-01"], expect_failure=True)
    if (day_value("profile_graph", "pilot_dbt") != "19" or unaffected_rows() != before):
        raise ValueError("bounded refresh or failed update changed a value outside its scope")
    print("PASS: one profile refreshed from 17 to 19 steps, peer retained 71; "
          "all unrelated daily rows matched and an unknown date was rejected.")


def check_step_aliases(lab, sql):
    def totals(graph):
        rows = sql(f"""select date,coalesce(steps::text,'missing') from {graph}.daily_metrics
                       where date between '2026-01-16' and '2026-01-21' order by date""")
        return [row.split('|')[1] for row in rows.splitlines()]

    before = ["missing", "21", "37", "missing", "7", "10"]
    if totals("profile_graph") != before or totals("peer_graph") != before:
        raise ValueError("full refresh lost or duplicated step measurements across offsets")
    sql("""set role pilot_dbt; insert into profile_source.metric_points(metric_name,date,source,qty,quality,units)
        values ('step_count','2026-01-19 01:20:00 +0200','Watch',37,'ok','count')""")
    build(lab, "profile", "--select", "+daily_metrics", affected_dates=["2026-01-19"])
    if totals("profile_graph") != ["missing", "21", "37", "37", "7", "10"]:
        raise ValueError("different measurement grains were collapsed")
    sql("""set role pilot_dbt; update profile_source.metric_points set health_record_id=1
           where date='2026-01-19 01:20:00 +0200'""")
    build(lab, "profile", "--select", "+daily_metrics", affected_dates=["2026-01-19"])
    if totals("profile_graph") != ["missing", "21", "missing", "37", "7", "10"] or totals("peer_graph") != before:
        raise ValueError(f"one-day alias totals: {totals('profile_graph')} peer {totals('peer_graph')}")
    if sql("select count(*) from profile_graph.hourly_metrics where metric_name='step_count' and hour='2026-01-18 23:00'") != "0":
        raise ValueError("old UTC offset alias left an hourly cache cell")
    sql("set role pilot_dbt; delete from profile_source.metric_points where date='2026-01-19 01:20:00 +0200'")
    sql("set role pilot_dbt; insert into profile_source.verified_step_withdrawals values ('2026-01-19 01:20:00 +0200','Watch')")
    build(lab, "profile", "--select", "+daily_metrics", affected_dates=["2026-01-19"])
    if totals("profile_graph") != before or totals("peer_graph") != before:
        raise ValueError("withdrawn local rendering failed to restore surviving UTC measurement")
    # This other source makes the whole wall hour ambiguous. The known Watch
    # cell can still be removed once its original aggregate is reproduced.
    sql("""set role pilot_dbt; insert into profile_source.metric_points(metric_name,date,source,qty,quality,units,health_record_id)
           values ('step_count','2026-01-18 23:40:00','Ring',3,'ok','count',1)""")
    build(lab, "profile", "--select", "+daily_metrics", affected_dates=["2026-01-18"])
    if totals("profile_graph") != before:
        raise ValueError("adding the smaller, ambiguous source changed the winning cell")
    sql("""set role pilot_dbt; delete from profile_source.verified_step_withdrawals;
           insert into profile_source.metric_points(metric_name,date,source,qty,quality,units,health_record_id)
           values ('step_count','2026-01-19 01:20:00 +0200','Watch',37,'ok','count',1)""")
    build(lab, "profile", "--select", "+daily_metrics", affected_dates=["2026-01-19"])
    if totals("profile_graph") != ["missing", "21", "3", "37", "7", "10"] or totals("peer_graph") != before:
        raise ValueError("ambiguous wall hour retained a proven alias or lost its independent source")
    wall = sql("""select source,avg_val from profile_graph.hourly_metrics
                  where metric_name='step_count' and hour='2026-01-18 23:00'""")
    if wall != "Ring|3":
        raise ValueError(f"reconciled ambiguous hourly publication {wall}")
    print("PASS: full and scoped offset aliases, unequal quantities, missing offsets, mixed-source fallback and withdrawal", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--reader-probe", type=Path, default=PILOT / "scripts/probe_profile_readers.py",
                        help="executable to verify product readers in each disposable profile")
    parser.add_argument("--capture-archive-bindings", action="store_true",
                        help="export typed stored-field samples from the disposable archive fixture")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="health-graph-") as tmp:
        cid = subprocess.check_output(["docker", "run", "--rm", "-d", "--tmpfs",
            "/var/lib/postgresql/data", "-p", "127.0.0.1::5432", "-e", f"POSTGRES_DB={DB}",
            "-e", "POSTGRES_PASSWORD=synthetic-only", IMAGE], text=True).strip()
        try:
            endpoint = subprocess.check_output(["docker", "port", cid, "5432/tcp"], text=True).strip()
            host, port = endpoint.rsplit(":", 1)
            if host != "127.0.0.1":
                raise ValueError("expected loopback fixture database")
            for _ in range(100):
                if subprocess.run(["docker", "exec", cid, "pg_isready", "-h", "127.0.0.1", "-U", "postgres"], capture_output=True).returncode == 0:
                    break
                time.sleep(.2)
            else:
                raise RuntimeError("temporary PostgreSQL failed to start")

            def sql(stmt):
                return subprocess.check_output(["docker", "exec", "-i", cid, "psql", "-U", "postgres",
                    "-d", DB, "-XqAt", "-v", "ON_ERROR_STOP=1"], input="set jit=off; " + stmt, text=True).strip()

            token = secrets.token_hex(16)
            prepare_inputs(sql, token)
            profiles = (("profile", "pilot_dbt", "peer_graph"), ("peer", "peer_builder", "profile_graph"))
            if args.capture_archive_bindings:
                install_and_probe(Path(tmp)/"profile", "profile", "pilot_dbt", "peer_graph", sql,
                                  host, port, token, args.reader_probe, capture_archive=True)
                return
            environments = [install_and_probe(Path(tmp) / name, name, role, peer_graph, sql,
                                              host, port, token, args.reader_probe)
                            for name, role, peer_graph in profiles]
            screen_probes(args.reader_probe, environments, "before", "sections")
            check_incremental(Path(tmp) / "profile", sql)
            screen_probes(args.reader_probe, environments, "after", "sections")

            days = json.loads(sql("select json_agg((current_date-n)::text order by n desc) from generate_series(0,14) n"))
            for name, role, _ in profiles:
                load_profile_fixture(sql, name + "_source", role, "profile_screen_energy.sql")
                build(Path(tmp) / name, name, "--select", "+daily_metrics", affected_dates=days)
            screen_probes(args.reader_probe, environments, "before", "energy")
            load_profile_fixture(sql, "profile_source", "pilot_dbt", "profile_screen_energy_correction.sql")
            build(Path(tmp) / "profile", "profile", "--select", "+daily_metrics", affected_dates=[days[-1]])
            screen_probes(args.reader_probe, environments, "after", "energy")
            check_step_aliases(Path(tmp) / "profile", sql)
            physical_days = ["2024-10-27", "2024-10-28"]
            for name, role, _ in profiles:
                load_profile_fixture(sql, name + "_source", role, "profile_physical_steps.sql")
                build(Path(tmp) / name, name, "--select", "+daily_metrics", affected_dates=physical_days)
            screen_probes(args.reader_probe, environments, "before", "physical_steps")
            sql("set role pilot_dbt; update profile_source.metric_points set qty=74 "
                "where metric_name='step_count' and source='iPhone' and date='2024-10-27 02:00:00 +0100'")
            build(Path(tmp) / "profile", "profile", "--select", "+daily_metrics", affected_dates=physical_days[:1])
            screen_probes(args.reader_probe, environments, "after", "physical_steps")
        finally:
            subprocess.run(["docker", "rm", "-fv", cid], check=True, stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
