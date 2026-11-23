#!/usr/bin/env python3
"""Exercise the deployable worker against a fresh, isolated PostgreSQL tenant."""

import argparse
from contextlib import contextmanager
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
        ('step_count','2026-01-01 10:00:00 +0000','Apple Watch',10);
        insert into settings(key,value) values('_cache_maintenance_v1','{"version":1,"generation":1,"dirty":{}}');""")
    sql((PROJECT / "fixtures/tenant_step_accumulation.sql").read_text())
    assert (
        sql("""select float4send(sum(qty order by n))<>float4send(sum(qty order by n desc))
        from (values(1,11.1::real),(2,0.15::real),(3,7.15::real)) samples(n,qty)""")
        == "t"
    )
    sql("alter database postgres set enable_presorted_aggregate=off")

    def check_step_sum():
        # Independent physical oracle: every distinct archive sample counts. Compare
        # binary REAL totals; the worker may not paper over a wrong input choice.
        assert (
            sql("""select float4send(steps)=(select float4send(sum(qty::float8)::real)
                 from metric_points where date like '2026-01-20%')
            from hdbt_daily_metrics where date='2026-01-20'""")
            == "t"
        ), "daily steps lost an observed sample during hourly cache proof"

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

    def worker(*, wait=True, extra=None, name=None, loop=False, expired_attempt=None, mounts=()):
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
            for source, target in mounts:
                cmd += ["-v", f"{source}:{target}:ro"]
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

    # Run the actual chart reader as the tenant role on the fixture's private
    # socket. The probe binary uses the same storage package as the service.
    probe = directory / "chart"
    machine = docker("exec", cid, "uname", "-m", capture_output=True).stdout.strip()
    goarch = {"aarch64": "arm64", "x86_64": "amd64"}[machine]
    subprocess.run(
        ["go", "test", "-c", "-o", str(probe), "./experiments/dbt_metrics/servingarchive"],
        cwd=ROOT,
        env=dict(os.environ, GOOS="linux", GOARCH=goarch, CGO_ENABLED="0"),
        check=True,
    )
    docker("cp", str(probe), f"{cid}:/tmp/chart", capture_output=True)

    def chart(expected):
        result = docker(
            "exec",
            "-e",
            f"PGUSER={role}",
            "-e",
            f"HDBT_SMOKE_EXPECT={expected}",
            cid,
            "/tmp/chart",
            "-test.v",
            "-test.run=^TestHDBTWorkerSmokeStepChart$",
            capture_output=True,
        )
        assert "--- PASS: TestHDBTWorkerSmokeStepChart " in result.stdout

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
    check_step_sum()
    other_xmin = sql("select xmin from hdbt_daily_metrics where date='2026-01-20'")

    sql("begin;" + mark(2) + "commit;")
    sql("alter table hdbt_w_daily add constraint fixture_refuse_20 check(steps<>20)")
    assert worker() == ("dbt_build", 1)
    assert actual() == "10" and sql("select last_status from hdbt_state") == "dbt_build"
    assert sql("select count(*) from hdbt_dirty") == "1"
    sql("alter table hdbt_w_daily drop constraint fixture_refuse_20")
    # The gate holds the final dbt model before publication. All timing-sensitive
    # assertions run while it is closed, independently of the worker lock timeout.
    sql("""create function fixture_pause_model() returns trigger language plpgsql as $$begin
        if current_setting('application_name') <> 'health-dbt-worker' then
            perform pg_advisory_xact_lock(9301,2);
            while not pg_try_advisory_xact_lock(9301,1) loop perform pg_sleep(0.05); end loop;
        end if;
        return null; end $$;
        create trigger fixture_pause_model after insert or update on hdbt_w_daily for each statement
        execute function fixture_pause_model();""")

    @contextmanager
    def model_gate():
        hold = subprocess.Popen(
            ["docker", "exec", "-i", cid, "psql", "-XqAt", "-U", "postgres", "-v", "ON_ERROR_STOP=1"],
            text=True,
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
        )
        try:
            hold.stdin.write("begin; select pg_advisory_xact_lock(9301,1) is not null;\n")
            hold.stdin.flush()
            assert hold.stdout.readline().strip() == "t"
            yield
        finally:
            hold.stdin.close()
            hold.wait(timeout=10)

    def wait_at_model_gate():
        wait_for(
            lambda: (
                sql("""select exists(select from pg_locks where locktype='advisory'
            and classid=9301 and objid=2 and granted)""")
                == "t"
            )
        )

    with model_gate():
        pending = worker(wait=False)
        wait_at_model_gate()
        require_status("busy")
        assert actual() == "10"
        sql("begin;" + mark(3) + "commit;")
    assert status(pending) == ("stale", 0)
    assert sql("select count(*) from hdbt_dirty") == "1"
    previous = sql("select attempt from hdbt_work_lease")
    name = "hdbt-crash-" + secrets.token_hex(6)
    with model_gate():
        crash = worker(wait=False, name=name)
        try:
            wait_at_model_gate()
            assert sql("select attempt from hdbt_work_lease") != previous
            if image:
                docker("kill", name, capture_output=True)
            else:
                crash.kill()
        finally:
            crash.wait(timeout=30)
    sql("drop trigger fixture_pause_model on hdbt_w_daily; drop function fixture_pause_model()")
    assert actual() == "10"
    require_status("published")
    assert actual() == "30" and sql("select source_generation from hdbt_state") == "3"
    work_xmin = sql("select xmin from hdbt_w_daily where date='2026-01-01'")
    require_status("dbt_build", expired_attempt=previous)
    assert work_xmin == sql("select xmin from hdbt_w_daily where date='2026-01-01'")
    assert sql("select count(*) from hdbt_dirty") == "0"
    assert other_xmin == sql("select xmin from hdbt_daily_metrics where date='2026-01-20'")
    sql("update hdbt_state set read_enabled=true")
    # The frozen old Watch hour survives a missing raw sample. A later explicit
    # repair receipt is required to withdraw it in favour of the remaining phone.
    sql("""begin;
        delete from metric_points where metric_name='step_count' and date like '2026-01-01%';
        insert into metric_points(metric_name,date,source,qty) values('step_count','2026-01-01 10:00:00 +0000','iPhone',100);
        update settings set value='{"version":1,"generation":4}' where key='_cache_maintenance_v1';
        insert into hdbt_dirty values('2026-01-01',4); commit""")
    chart(30)
    require_status("published")
    chart(30)
    assert actual() == "30"
    sql("""begin;
        insert into hdbt_step_repairs values('2026-01-01 10:00:00 +0000','Apple Watch',5);
        update settings set value='{"version":1,"generation":5}' where key='_cache_maintenance_v1';
        insert into hdbt_dirty values('2026-01-01',5); commit""")
    chart(100)
    require_status("published")
    chart(100)
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
    sql("alter database postgres set enable_presorted_aggregate=on")
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
        check_step_sum()
        reader.stdin.write("select steps::int from hdbt_daily_metrics where date='2026-01-01'; commit;\n")
        reader.stdin.flush()
        assert reader.stdout.readline().strip() == "100"
    finally:
        reader.stdin.close()
        reader.wait(timeout=10)
    # Day 3 is inside the first core day's output footprint; its own alias on
    # day 5 lies outside that footprint. Retain both its ACK and repair receipt.
    sql("""begin; insert into metric_points(metric_name,date,source,qty) values
        ('step_count','2026-01-03 10:00:00 +0000','retiring watch',15),
        ('step_count','2026-01-05 01:30:00 +1400','Alias Watch',80);
        update settings set value='{"version":1,"generation":7}' where key='_cache_maintenance_v1';
        insert into hdbt_dirty values('2026-01-03',7),('2026-01-05',7); commit""")
    require_status("building")
    require_status("published")
    assert sql("select steps::int from hdbt_daily_metrics where date='2026-01-05'") == "80"
    sql("""begin; insert into metric_points(metric_name,date,source,qty)
        values('step_count','2026-01-03 23:30:00 -1200','Alias Watch',80);
        delete from metric_points where source='retiring watch';
        insert into hdbt_step_repairs values('2026-01-03 10:00:00 +0000','retiring watch',8);
        update settings set value='{"version":1,"generation":8}' where key='_cache_maintenance_v1';
        insert into hdbt_dirty values('2026-01-01',8),('2026-01-03',8); commit""")
    require_status("building")
    assert sql("select date from hdbt_dirty") == "2026-01-03"
    assert sql("select count(*) from hdbt_step_repairs where generation=8") == "1"
    assert sql("select source_generation from hdbt_state where not ready") == "7"
    require_status("published")
    assert sql("select steps::int from hdbt_daily_metrics where date='2026-01-03'") == "80"
    assert sql("select count(*) from hdbt_daily_metrics where date='2026-01-05' and steps>0") == "0"
    assert sql("select count(*) from hdbt_step_repairs") == "0"
    sql("""begin; insert into metric_points(metric_name,date,source,qty) values
        ('step_count','0001-01-01 12:00:00 +0000','Watch',11),
        ('step_count','9999-12-31 12:00:00 +0000','Watch',19);
        update settings set value='{"version":1,"generation":9}' where key='_cache_maintenance_v1';
        insert into hdbt_dirty values('0001-01-01',9),('9999-12-31',9); commit""")
    require_status("building")
    require_status("published")
    assert (
        sql(
            "select steps::int from hdbt_daily_metrics where date in ('0001-01-01','9999-12-31') order by date"
        )
        == "11\n19"
    )

    # Generate a test-only trust root and serve TLS on the same isolated database.
    cert, key = directory / "tls.crt", directory / "tls.key"
    subprocess.run(
        [
            "openssl",
            "req",
            "-x509",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-days",
            "1",
            "-subj",
            "/CN=fixture",
            "-addext",
            "subjectAltName=DNS:fixture,IP:127.0.0.1",
            "-keyout",
            str(key),
            "-out",
            str(cert),
        ],
        capture_output=True,
        check=True,
    )
    cert.chmod(0o444)
    for file in (cert, key):
        docker("cp", str(file), f"{cid}:/tmp/{file.name}", capture_output=True)
    docker("exec", cid, "chown", "postgres:postgres", "/tmp/tls.key", capture_output=True)
    docker("exec", cid, "chmod", "0600", "/tmp/tls.key", capture_output=True)
    sql(
        "alter system set ssl_cert_file='/tmp/tls.crt'; alter system set ssl_key_file='/tmp/tls.key';"
        "alter system set ssl=on; select pg_reload_conf()"
    )
    wait_for(lambda: sql("show ssl") == "on")
    verified = secret.read_text().replace("sslmode=disable", "sslmode=verify-full")

    def set_dsn(contents):
        secret.chmod(0o600)
        secret.write_text(contents)
        secret.chmod(0o444)

    set_dsn(verified)
    assert worker()[1] != 0  # The private test CA is outside the stock trust store.
    roots = (
        {"mounts": [(cert, "/etc/ssl/certs/ca-certificates.crt")]}
        if image
        else {"extra": {"SSL_CERT_FILE": str(cert)}}
    )
    sql("begin;" + mark(10) + "commit;")
    require_status("published", **roots)  # Both the Python session and dbt verify the server.
    mounted = "/tmp/fixture-ca.pem" if image else str(cert)
    set_dsn(verified + "&sslrootcert=" + mounted)
    require_status("idle", mounts=[(cert, mounted)] if image else [])
    print(
        "PASS bootstrap/re-enrollment, tenant ACL/checksum, scoped sync, stale/failed/crashed builds, "
        "withdrawal proof, retained reader, expired lease, SQL cancellation, scoped ACK, chart parity, boundary dates and TLS"
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
