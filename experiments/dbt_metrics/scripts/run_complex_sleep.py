#!/usr/bin/env python3
"""Exercise the sleep dbt shadows with Go oracles in a disposable synthetic PostgreSQL."""
import json
import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time

PILOT = Path(__file__).resolve().parents[1]
ROOT = PILOT.parents[1]
SEM = PILOT / "semantic"


def run(*args, **options):
    return subprocess.run(args, cwd=ROOT, check=True, text=True, **options)


def main():
    token = secrets.token_hex(16)
    # Docker assigns the host port and returns the ID of the container we own.
    cid = run("docker", "run", "--rm", "-d", "--tmpfs", "/var/lib/postgresql/data",
              "-p", "127.0.0.1::5432", "-e", "POSTGRES_PASSWORD=synthetic-only", "-e",
              "POSTGRES_DB=sem_synthetic", "postgres:16-alpine", "-c", "timezone=Europe/Belgrade",
              capture_output=True).stdout.strip()
    try:
        for _ in range(100):
            if subprocess.run(["docker","exec",cid,"pg_isready","-h","127.0.0.1"],capture_output=True).returncode == 0:
                break
            time.sleep(.2)
        else:
            raise RuntimeError("synthetic PostgreSQL did not start")
        port = run("docker","port",cid,"5432/tcp",capture_output=True).stdout.strip().removeprefix("127.0.0.1:")
        if not port.isdecimal():
            raise RuntimeError("expected a single loopback mapping")

        def sql(statement):
            return run("docker","exec","-i",cid,"psql","-XAtq","-U","postgres","-d","sem_synthetic","-v","ON_ERROR_STOP=1",
                       input=statement, stdout=subprocess.PIPE).stdout.strip()

        for name in ("inputs.sql","synthetic.sql","complex_sleep_fixture.sql"):
            sql((SEM / name).read_text())
        sql(f"""
          create role shadow_dbt login password 'synthetic-only';
          create role shadow_reader login password 'synthetic-only';
          create schema dbt_output authorization shadow_dbt;
          grant usage on schema source_data,dbt_output to shadow_reader;
          grant usage on schema source_data to shadow_dbt;
          grant select on all tables in schema source_data to shadow_reader,shadow_dbt;
          alter default privileges for role shadow_dbt in schema dbt_output grant select on tables to shadow_reader;
          create table public.sleep_fixture_identity(token text not null);
          insert into public.sleep_fixture_identity values ('{token}');
          grant select on public.sleep_fixture_identity to shadow_reader;
        """)
        with tempfile.TemporaryDirectory(prefix="sleep-dbt-") as tmp:
            lab=Path(tmp)
            (lab/"profiles.yml").write_text(f"""health_dbt_metrics_pilot:
  target: fixture
  outputs:
    fixture:
      type: postgres
      host: 127.0.0.1
      port: {port}
      user: shadow_dbt
      password: synthetic-only
      dbname: sem_synthetic
      schema: dbt_output
      threads: 1
""")
            env={k:v for k,v in os.environ.items() if not k.startswith(("PG","SEMANTIC_SHADOW_PG_")) and k!="DATABASE_URL"}
            env.update(DBT_SEND_ANONYMOUS_USAGE_STATS="false",SLEEP_DOCKER_TOKEN=token,SEMANTIC_SHADOW_PG_PORT=port)
            audit = env.get("HEALTH_METRIC_AUDIT_DIR")
            extra_models = []
            if audit:
                sql((PILOT / "metric_schema/shadow_inputs.sql").read_text())
                extra_models.append("shadow_sleep_daily_v1")
            run(PILOT/".venv/bin/dbt","build","--project-dir",PILOT,"--profiles-dir",lab,
                "--target-path",lab/"target","--log-path",lab/"logs","--vars",
                json.dumps(dict(sleep_duration_reference=True, semantic_shadow=True, complex_sleep_parity=True, sleep_daily_shadow=bool(audit))),
                "--select","sleep_duration_reference_v1","semantic_*","sleep_balance_period_v1","sleep_duration_balance_v1","sleep_balance_history_v1","wake_confirmation_v1","wake_candidate_v1","packet_sleep_selection_v1",*extra_models,env=env)
            if audit:
                run(PILOT/".venv/bin/python", PILOT/"scripts/capture_bound_metrics.py", port, env=env)
                run(PILOT/".venv/bin/python", PILOT/"scripts/capture_bound_metrics.py", port, "complex_sleep", env=env)
            for name in ("sleep_cases.sql","sleep_continuity_cases.sql","night_quality_cases.sql","hrv_cases.sql"):
                sql((SEM/name).read_text())
            raw=sql("""select json_agg(json_build_object(
                'Profile',c.profile,'Capture',c.capture,'Zone',zone,'DecisionAt',decision_at,'CandidateWake',c.candidate_wake,
                'LatestIngest',latest_ingest,'QuietSince',quiet_since,'Signal',signal,'Steps',steps,'TypicalMinute',typical_minute,
                'Reason',r.reason,'Ready',r.ready,'Confidence',r.confidence))
                from source_data.wake_capture c left join dbt_output.wake_confirmation_v1 r using(profile,capture)""")
            wake_file=lab/"wake.json"
            wake_file.write_text(json.dumps(json.loads(raw)))
            env["SYNTHETIC_WAKE_PARITY_FILE"]=str(wake_file)
            run("go","test","./experiments/dbt_metrics","-count=1","-v","-run",
                "^Test(SemanticSleepParity|SemanticNightQualityParity|ComplexSleepBalanceParity)$",env=env)
            env.update(PACKET_CHOICE_DBT="1", HEALTH_DB_TESTS="1", READINESS_TEST_DSN=f"postgres://postgres:synthetic-only@127.0.0.1:{port}/sem_synthetic?sslmode=disable")
            run("go","test","./internal/storage","-count=1","-v","-run",
                "^Test(MetricRuntimeSleepArchitectureCapture|PreferredSleepSourceReaders|PreferredSleepSourceSnapshots|SleepDurationComparisonSnapshots|RevisionBoundSleepSelectionWithdrawal|WakeConfirmationDBTParity|WakeCandidateDBTParity|ComputeMorningWakeStatusPersistsSleepInputClockAcrossDuplicateSync|BackfillWakeTimesIsIdempotentAndLeavesRowsProvisional)$", env=env)
    finally:
        run("docker","stop","-t","1",cid,stdout=subprocess.DEVNULL)


if __name__ == "__main__":
    main()
