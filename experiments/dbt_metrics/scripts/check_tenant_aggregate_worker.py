#!/usr/bin/env python3
"""Exercise the deployable worker against a fresh, isolated PostgreSQL tenant."""

import argparse
import json
import os
from pathlib import Path
import secrets
import re
import subprocess
import sys
import tempfile
import time
import uuid

from run_pilot import IMAGE

PROJECT = Path(__file__).resolve().parents[1]
ROOT = PROJECT.parents[1]
WORKER = PROJECT / "scripts/tenant_aggregate_worker.py"


def docker(*args, **kwargs):
    return subprocess.run(["docker", *args], text=True, check=True, **kwargs)


def wait_for(check):
    until = time.monotonic() + 120
    while not check():
        if time.monotonic() > until:
            raise AssertionError("fixture barrier timed out")
        time.sleep(0.15)


def exercise(image, directory, network, cid):
    platform = (
        docker(
            "image", "inspect", "--format", "{{.Os}}/{{.Architecture}}", image, capture_output=True
        ).stdout.strip()
        if image
        else None
    )
    tenant = uuid.uuid4()
    role = "health_t_" + tenant.hex
    schema = "test_tenant"
    password = secrets.token_hex(20)

    def sql(stmt, *, tenant_path=True):
        prefix = f"set search_path={schema},pg_catalog;" if tenant_path else ""
        return docker(
            "exec",
            "-i",
            cid,
            "psql",
            "-XqAt",
            "-v",
            "ON_ERROR_STOP=1",
            "-U",
            "postgres",
            input=prefix + stmt,
            capture_output=True,
        ).stdout.strip()

    sql((PROJECT / "fixtures/schema.sql").read_text(), tenant_path=False)
    sql(f"""create role {role} login password '{password}'; create schema {schema} authorization {role};
        create schema other_private; create table other_private.__tenant_identity(singleton boolean);""")
    for original, tables in (
        ("source_data", ("metric_points", "health_records")),
        ("go_oracle", ("settings", "daily_scores", "hourly_metrics")),
    ):
        for table in tables:
            sql(
                f"alter table {original}.{table} set schema {schema}; alter table {schema}.{table} owner to {role};"
                f"revoke all on {schema}.{table} from pilot_dbt"
            )
    digest = re.search(r'^SCHEMA_CHECKSUM = "([0-9a-f]{64})"$', WORKER.read_text(), re.MULTILINE).group(1)
    sql(
        f"""set role {role};
        create table __tenant_identity(singleton boolean primary key,tenant_id uuid,schema_contract_version integer,schema_contract_checksum text);
        insert into __tenant_identity values(true,'{tenant}',17,'{digest}');
        """
        + (ROOT / "internal/storage/sql/hdbt_v17.sql").read_text()
    )
    sql("""insert into metric_points(metric_name,date,source,qty) values
        ('step_count','2026-01-01 10:00:00 +0000','Apple Watch',10),
        ('step_count','2026-01-20 10:00:00 +0000','Apple Watch',70);
        insert into settings(key,value) values('_cache_maintenance_v1','{"version":1,"generation":1,"dirty":{}}');""")
    dsn_host, dsn_port = (
        ("fixture", "5432")
        if image
        else docker("port", cid, "5432/tcp", capture_output=True).stdout.strip().rsplit(":", 1)
    )
    secret = directory / "dsn"
    secret.write_text(f"postgresql://{role}:{password}@{dsn_host}:{dsn_port}/postgres?sslmode=disable")
    secret.chmod(0o444)
    env = dict(
        HEALTH_DBT_WORKER_ENABLED="true",
        HEALTH_DBT_TENANT_ID=str(tenant),
        HEALTH_DBT_TENANT_SCHEMA=schema,
        HEALTH_DBT_BATCH_DAYS="1",
        HEALTH_DBT_TENANT_DSN_FILE="/run/tenant-dsn" if image else str(secret),
    )

    def worker(*, wait=True, extra=None, name=None, loop=False, expired_attempt=None):
        active = dict(env, **(extra or {}))
        args = [] if loop else ["--once"]
        if expired_attempt:
            # Replay an orphaned real dbt invocation after a newer workspace exists.
            entry = "/app/experiments/dbt_metrics/scripts" if image else str(WORKER.parent)
            args = [
                "-c",
                """
import json,sys
sys.path.insert(0, sys.argv[1])
from tenant_aggregate_worker import build,configuration,WorkerError
schema,_,config,_ = configuration()
try:
    build(config,schema,["2026-01-01"],sys.argv[2])
except WorkerError as error:
    print(json.dumps({"status":str(error)}))
else:
    print(json.dumps({"status":"unexpected_success"}))
""",
                entry,
                expired_attempt,
            ]
        if image:
            cmd = [
                "docker",
                "run",
                "--rm",
                "--platform",
                platform,
                "--network",
                network,
                "--read-only",
                "--tmpfs",
                "/tmp:rw,nosuid,nodev",
                "-v",
                f"{secret}:/run/tenant-dsn:ro",
            ]
            if expired_attempt:
                cmd += ["--entrypoint", "/opt/dbt/bin/python"]
            if name:
                cmd += ["--name", name]
            for key, value in active.items():
                cmd += ["-e", f"{key}={value}"]
            cmd += [image, *args]
            child_env = os.environ.copy()
        else:
            cmd = [sys.executable, *([] if expired_attempt else [str(WORKER)]), *args]
            child_env = dict(os.environ, TMPDIR=str(directory), **active)
        proc = subprocess.Popen(cmd, env=child_env, text=True, stdout=subprocess.PIPE)
        return status(proc) if wait else proc

    def status(proc):
        stdout, _ = proc.communicate(timeout=120)
        return json.loads(stdout.strip())["status"], proc.returncode

    def require_status(want, **args):
        got = worker(**args)
        assert got == (want, 0), (want, got)

    def actual():
        return sql("select steps::int from hdbt_daily_metrics where date='2026-01-01'")

    def mark(gen):
        return f"""update metric_points set qty={10 * gen} where date like '2026-01-01%';
            update settings set value='{{"version":1,"generation":{gen},"dirty":{{}}}}' where key='_cache_maintenance_v1';
            insert into hdbt_dirty values('2026-01-01',{gen}) on conflict(date) do update set generation=excluded.generation;"""

    require_status("disabled")
    sql("update __tenant_identity set schema_contract_checksum='unknown'")
    assert worker()[1] != 0
    sql(f"update __tenant_identity set schema_contract_checksum='{digest}'")
    assert worker(extra={"HEALTH_DBT_TENANT_SCHEMA": "other_private"})[1] != 0
    sql("update hdbt_state set shadow_enabled=true")
    sql("""create function fixture_block_capture() returns trigger language plpgsql as $$
        begin perform pg_sleep(45); return null; end $$;
        create trigger fixture_block_capture before truncate on hdbt_i_points
        for each statement execute function fixture_block_capture();""")
    stop_name = "hdbt-stop-" + secrets.token_hex(6)
    slow = worker(wait=False, name=stop_name, loop=True)
    try:
        wait_for(
            lambda: (
                sql("""select exists(select from pg_stat_activity
            where application_name='health-dbt-worker' and wait_event='PgSleep')""")
                == "t"
            )
        )
        if image:
            docker("kill", "--signal", "TERM", stop_name, capture_output=True)
        else:
            slow.terminate()
        assert slow.wait(timeout=8) == 0
    finally:
        if slow.poll() is None:
            if image:
                docker("kill", stop_name, capture_output=True)
            else:
                slow.kill()
            slow.wait(timeout=10)
        sql("drop trigger fixture_block_capture on hdbt_i_points; drop function fixture_block_capture()")
    require_status("building")
    assert sql("select ready from hdbt_state") == "f"
    require_status("published")
    assert actual() == "10" and sql("select source_generation from hdbt_state") == "1"
    require_status("idle")
    other_xmin = sql("select xmin from hdbt_daily_metrics where date='2026-01-20'")

    sql("begin;" + mark(2) + "commit;")
    sql("alter table hdbt_w_daily add constraint fixture_refuse_20 check(steps<>20)")
    assert worker() == ("dbt_build", 1)
    assert actual() == "10" and sql("select last_status from hdbt_state") == "dbt_build"
    assert sql("select count(*) from hdbt_dirty") == "1"
    sql("alter table hdbt_w_daily drop constraint fixture_refuse_20")
    # Hold generation 3 uncommitted as the worker builds generation 2. Release
    # the source writer only once the worker is waiting on its publication fence.
    hold = subprocess.Popen(
        ["docker", "exec", "-i", cid, "psql", "-XqAt", "-U", "postgres", "-v", "ON_ERROR_STOP=1"],
        text=True,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
    )
    try:
        hold.stdin.write(f"set search_path={schema},pg_catalog; begin;" + mark(3) + "select 'locked';\n")
        hold.stdin.flush()
        assert hold.stdout.readline().strip() == "locked"
        pending = worker(wait=False)
        wait_for(
            lambda: (
                sql("""select exists(select from pg_stat_activity where application_name='health-dbt-worker'
            and wait_event_type='Lock' and query ilike '%from settings%for share%')""")
                == "t"
            )
        )
        require_status("busy")
        assert actual() == "10"
        hold.stdin.write("commit;\n")
        hold.stdin.close()
        hold.wait(timeout=10)
        assert status(pending) == ("stale", 0)
    finally:
        if hold.poll() is None:
            hold.kill()
            hold.wait()
    assert sql("select count(*) from hdbt_dirty") == "1"
    previous = sql("select attempt from hdbt_work_lease")
    name = "hdbt-crash-" + secrets.token_hex(6)
    crash = worker(wait=False, name=name)
    try:
        wait_for(lambda: sql("select attempt from hdbt_work_lease") != previous)
        if image:
            docker("kill", name, capture_output=True)
        else:
            crash.kill()
    finally:
        crash.wait(timeout=30)
    assert actual() == "10"
    require_status("published")
    assert actual() == "30" and sql("select source_generation from hdbt_state") == "3"
    work_xmin = sql("select xmin from hdbt_w_daily where date='2026-01-01'")
    require_status("dbt_build", expired_attempt=previous)
    assert work_xmin == sql("select xmin from hdbt_w_daily where date='2026-01-01'")
    assert sql("select count(*) from hdbt_dirty") == "0"
    assert other_xmin == sql("select xmin from hdbt_daily_metrics where date='2026-01-20'")
    # The frozen old Watch hour survives a missing raw sample. A later explicit
    # repair receipt is required to withdraw it in favour of the remaining phone.
    sql("""begin;
        delete from metric_points where metric_name='step_count' and date like '2026-01-01%';
        insert into metric_points(metric_name,date,source,qty) values('step_count','2026-01-01 10:00:00 +0000','iPhone',100);
        update settings set value='{"version":1,"generation":4}' where key='_cache_maintenance_v1';
        insert into hdbt_dirty values('2026-01-01',4); commit""")
    require_status("published")
    assert actual() == "30"
    sql("""begin;
        insert into hdbt_step_repairs values('2026-01-01 10:00:00 +0000','Apple Watch',5);
        update settings set value='{"version":1,"generation":5}' where key='_cache_maintenance_v1';
        insert into hdbt_dirty values('2026-01-01',5); commit""")
    require_status("published")
    assert actual() == "100" and sql("select count(*) from hdbt_step_repairs") == "0"
    sql("update hdbt_state set read_enabled=true")
    reader = subprocess.Popen(
        ["docker", "exec", "-i", cid, "psql", "-XqAt", "-U", "postgres", "-v", "ON_ERROR_STOP=1"],
        text=True,
        stdin=subprocess.PIPE,
        stdout=subprocess.PIPE,
    )
    reader.stdin.write(
        f"set search_path={schema},pg_catalog; begin isolation level repeatable read;"
        "select ready and read_enabled from hdbt_state;\n"
    )
    reader.stdin.flush()
    assert reader.stdout.readline().strip() == "t"
    sql("update hdbt_state set shadow_enabled=false")
    assert sql("select not ready and not read_enabled and not initialized from hdbt_state") == "t"
    require_status("disabled")
    try:
        sql("""begin; update metric_points set qty=170 where source='iPhone';
            update settings set value='{"version":1,"generation":6}' where key='_cache_maintenance_v1';
            commit; update hdbt_state set shadow_enabled=true""")
        assert sql("select count(*) from hdbt_dirty") == "0"
        require_status("building")
        assert sql("select ready or read_enabled from hdbt_state") == "f"
        require_status("published")
        assert actual() == "170" and sql("select source_generation from hdbt_state") == "6"
        reader.stdin.write("select steps::int from hdbt_daily_metrics where date='2026-01-01'; commit;\n")
        reader.stdin.flush()
        assert reader.stdout.readline().strip() == "100"
    finally:
        reader.stdin.close()
        reader.wait(timeout=10)
    print(
        "PASS bootstrap/re-enrollment, tenant ACL/checksum, scoped sync, stale/failed/crashed builds, "
        "withdrawal proof, retained reader, expired work lease, SQL cancellation and disabled rollback"
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--worker-image")
    args = parser.parse_args()
    network = "health-dbt-test-" + secrets.token_hex(8)
    docker("network", "create", *(["--internal"] if args.worker_image else []), network, capture_output=True)
    try:
        cid = docker(
            "run",
            "--rm",
            "-d",
            "--network",
            network,
            "--network-alias",
            "fixture",
            "--tmpfs",
            "/var/lib/postgresql/data",
            *([] if args.worker_image else ["-p", "127.0.0.1::5432"]),
            "-e",
            "POSTGRES_PASSWORD=synthetic-only",
            IMAGE,
            capture_output=True,
        ).stdout.strip()
        try:
            wait_for(
                lambda: (
                    subprocess.run(
                        ["docker", "exec", cid, "pg_isready", "-h", "127.0.0.1"], capture_output=True
                    ).returncode
                    == 0
                )
            )
            with tempfile.TemporaryDirectory(prefix="health-dbt-integration-") as tmp:
                exercise(args.worker_image, Path(tmp), network, cid)
        finally:
            docker("rm", "-f", cid, capture_output=True)
    finally:
        docker("network", "rm", network, capture_output=True)


if __name__ == "__main__":
    main()
