#!/usr/bin/env python3
"""Compare source selection in Go and dbt on a disposable, synthetic tenant."""
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time

PILOT = Path(__file__).resolve().parents[1]
ROOT = PILOT.parents[1]
MODELS = ["wake_candidate_v1", "sleep_regularity_index_v1", "packet_sleep_selection_v1"]
TESTS = ["TestWakeCandidateDBTParity", "TestSleepRegularityShadow", "TestRevisionBoundSleepSelectionWithdrawal"]


def run(*command, **options):
    return subprocess.run(command, check=True, cwd=ROOT, text=True, **options)


def main():
    dbt = os.environ.get("DBT_BIN", str(PILOT / ".venv/bin/dbt"))
    token = secrets.token_hex(16)
    container = run("docker", "run", "--rm", "-d", "--tmpfs", "/var/lib/postgresql/data",
                    "-p", "127.0.0.1::5432", "-e", "POSTGRES_DB=source_fixture",
                    "-e", "POSTGRES_PASSWORD=synthetic-only", "postgres:16-alpine",
                    "-c", "timezone=Europe/Belgrade", capture_output=True).stdout.strip()
    try:
        for _ in range(100):
            ready = subprocess.run(["docker", "exec", container, "pg_isready", "-h", "127.0.0.1"],
                                   capture_output=True)
            if ready.returncode == 0:
                break
            time.sleep(0.2)
        else:
            raise RuntimeError("synthetic PostgreSQL did not start")
        address = run("docker", "port", container, "5432/tcp", capture_output=True).stdout.strip()
        host, port = address.split(":")
        if host != "127.0.0.1" or not port.isdecimal():
            raise RuntimeError("expected one ephemeral loopback port")
        sql = (PILOT / "fixtures/preferred_sleep_source.sql").read_text()
        sql += f"""
            create table public.sleep_fixture_identity(token text);
            insert into public.sleep_fixture_identity values ('{token}');
            create role shadow_dbt login password 'synthetic-only';
            create schema dbt_output authorization shadow_dbt;
            grant usage on schema source_data to shadow_dbt;
            grant select on all tables in schema source_data to shadow_dbt;
        """
        run("docker", "exec", "-i", container, "psql", "-Xq", "-v", "ON_ERROR_STOP=1",
            "-U", "postgres", "-d", "source_fixture", input=sql)
        with tempfile.TemporaryDirectory(prefix="preferred-sleep-") as tmp:
            lab = Path(tmp)
            (lab / "profiles.yml").write_text(f"""health_dbt_metrics_pilot:
  target: fixture
  outputs:
    fixture:
      type: postgres
      host: 127.0.0.1
      port: {port}
      user: shadow_dbt
      password: synthetic-only
      dbname: source_fixture
      schema: dbt_output
      threads: 1
""")
            env = {k: v for k, v in os.environ.items() if not k.startswith("PG") and k != "DATABASE_URL"}
            env.update(DBT_SEND_ANONYMOUS_USAGE_STATS="false", PREFERRED_SLEEP_FIXTURE_TOKEN=token,
                       HEALTH_DB_TESTS="1", PACKET_CHOICE_DBT="1",
                       READINESS_TEST_DSN=f"postgres://postgres:synthetic-only@127.0.0.1:{port}/source_fixture?sslmode=disable")
            run(dbt, "build", "--project-dir", PILOT, "--profiles-dir", lab, "--target-path", lab / "target",
                "--log-path", lab / "logs", "--vars", "{preferred_sleep_source_shadow: true}", "--select", *MODELS, env=env)
            result = subprocess.run(["go", "test", "./internal/storage", "-count=1", "-json", "-run",
                                     "^(" + "|".join(TESTS) + ")$"], text=True, cwd=ROOT, env=env, stdout=subprocess.PIPE)
            events = [json.loads(line) for line in result.stdout.splitlines()]
            for event in events:
                if event["Action"] == "output":
                    print(event["Output"], end="")
            result.check_returncode()
            passed = {e.get("Test") for e in events if e["Action"] == "pass"}
            if not set(TESTS) <= passed:
                raise RuntimeError(f"missing passing Go comparisons: {set(TESTS) - passed}")
    finally:
        run("docker", "stop", "-t", "1", container, stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
