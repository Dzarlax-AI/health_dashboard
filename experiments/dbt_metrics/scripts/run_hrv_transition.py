#!/usr/bin/env python3
"""Run the synthetic Go/dbt HRV invalidation transition in disposable Docker PG."""
from __future__ import annotations

import os
from pathlib import Path
import secrets
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[3]
HERE = Path(__file__).resolve().parents[1]
IMAGE = "postgres@sha256:b0f9560a2de083e2cc7382e75f808c7381a32852a7ec49117deedb300e552b24"
PASSWORD = "synthetic-only"
DB = "hp_dbt_pilot"
LABEL = "codex.health-dbt-pilot"


def run(args: list[str], *, check: bool = True, capture: bool = False,
        input_text: str | None = None, env: dict[str, str] | None = None,
        cwd: Path | None = None) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, check=check, capture_output=capture, text=True,
                          input=input_text, env=env, cwd=cwd)


def docker(*args: str, **kwargs) -> subprocess.CompletedProcess[str]:
    return run(["docker", *args], **kwargs)


def main() -> int:
    token = "hp-transition-" + secrets.token_hex(8)
    name = token
    port = 0
    container_id = ""
    marker_created = False
    failure: BaseException | None = None
    with tempfile.TemporaryDirectory(prefix="health-hrv-transition-") as tmp:
        lab = Path(tmp)
        profile_dir = lab / "profiles"
        profile_dir.mkdir()
        (profile_dir / "profiles.yml").write_text("""health_dbt_metrics_pilot:
  target: local
  outputs:
    local:
      type: postgres
      host: 127.0.0.1
      port: "{{ env_var('DBT_PILOT_PORT') | int }}"
      user: pilot_dbt
      password: synthetic-only
      dbname: hp_dbt_pilot
      schema: dbt_output
      threads: 1
      search_path: "dbt_output,source_data,public"
""")
        try:
            container_id = docker("run", "-d", "--name", name, "--label", f"{LABEL}={token}",
                                  "-e", "POSTGRES_USER=pilot_admin", "-e", f"POSTGRES_PASSWORD={PASSWORD}",
                                  "-e", f"POSTGRES_DB={DB}", "-p", "127.0.0.1::5432", IMAGE,
                                  capture=True).stdout.strip()
            label = docker("inspect", "-f", f"{{{{ index .Config.Labels \"{LABEL}\" }}}}",
                           container_id, capture=True).stdout.strip()
            if not container_id or label != token:
                raise RuntimeError("disposable database identity marker mismatch")
            published = docker("port", container_id, "5432/tcp", capture=True).stdout.strip()
            if not published.startswith("127.0.0.1:"):
                raise RuntimeError("temporary PostgreSQL port is not loopback-bound")
            port = int(published.rsplit(":", 1)[1])
            for _ in range(60):
                ready = docker("exec", container_id, "pg_isready", "-h", "127.0.0.1", "-U", "pilot_admin", "-d", DB,
                               check=False, capture=True)
                if ready.returncode == 0:
                    break
                time.sleep(1)
            else:
                raise RuntimeError("temporary PostgreSQL did not become ready")

            docker("exec", "-i", container_id, "psql", "-v", "ON_ERROR_STOP=1", "-U",
                   "pilot_admin", "-d", DB, input_text=(HERE / "fixtures/schema.sql").read_text())
            sql = f"""CREATE TABLE public.pilot_identity(token text NOT NULL);
INSERT INTO public.pilot_identity VALUES ('{token}');
CREATE TABLE source_data.daily_hrv(date text PRIMARY KEY, hrv_avg real, repair_pending boolean not null default false);
CREATE VIEW source_data.hrv_point AS
 SELECT date,source,qty,quality,'ms'::text AS unit,'unknown'::text AS method,
        'test'::text AS channel,'timestamp'::text AS time_kind,
        date::timestamptz AS start_at,NULL::timestamptz AS end_at
 FROM source_data.metric_points WHERE metric_name='heart_rate_variability';
GRANT SELECT ON source_data.daily_hrv, source_data.hrv_point TO pilot_dbt;
"""
            docker("exec", "-i", container_id, "psql", "-v", "ON_ERROR_STOP=1", "-U",
                   "pilot_admin", "-d", DB, input_text=sql)
            marker_created = True

            env = {k: v for k, v in os.environ.items()
                   if not k.startswith("PG") and k not in {"DATABASE_URL", "DBT_PILOT_ENABLE",
                                                            "DBT_PILOT_DSN", "DBT_PILOT_TOKEN"}}
            env.update({"DBT_PILOT_PORT": str(port), "DBT_SEND_ANONYMOUS_USAGE_STATS": "false"})
            env["GOCACHE"] = str(lab / "go-cache")
            dbt = HERE / ".venv/bin/dbt"
            if not dbt.is_file():
                raise RuntimeError("dbt environment missing; install experiments/dbt_metrics requirements first")
            run([str(dbt), "run", "--project-dir", str(HERE), "--profiles-dir", str(profile_dir),
                 "--vars", "{semantic_shadow: true, sleep_duration_reference: true}",
                 "--select", "+semantic_hrv_daily_v1", "--target-path", str(lab / "target"),
                 "--log-path", str(lab / "logs"), "--no-partial-parse"],
                env=env, cwd=ROOT)

            dsn = f"postgres://pilot_admin:{PASSWORD}@127.0.0.1:{port}/{DB}?sslmode=disable"
            env.update({"DBT_PILOT_ENABLE": "synthetic-only", "DBT_PILOT_DSN": dsn,
                        "DBT_PILOT_TOKEN": token})
            run(["go", "test", "./internal/storage", "-run",
                 "^TestDBTHRVInvalidationTransition$", "-count=1", "-v"], env=env, cwd=ROOT)
            marker = docker("exec", container_id, "psql", "-At", "-U", "pilot_admin", "-d", DB,
                            "-c", "SELECT token FROM public.pilot_identity", capture=True).stdout.strip()
            if marker != token:
                raise RuntimeError("synthetic PostgreSQL identity changed before storage tests")
            env.update({"HEALTH_DB_TESTS": "1", "READINESS_TEST_DSN": dsn})
            run(["go", "test", "./internal/storage", "-run",
                 "^Test(HRV|DailyAverageChart|StepWithdrawalFencesConcurrentPublication|QualityAuditClearsHRV|AppleHealthXMLImport|AcceptedNightIngest)",
                 "-count=1", "-v"], env=env, cwd=ROOT)
        except BaseException as exc:
            failure = exc
            raise
        finally:
            if container_id:
                try:
                    label = docker("inspect", "-f", f"{{{{ index .Config.Labels \"{LABEL}\" }}}}",
                                   container_id, capture=True).stdout.strip()
                    if label != token:
                        raise RuntimeError("refusing to remove a container without this run's label")
                    if marker_created:
                        actual = docker("exec", container_id, "psql", "-At", "-U", "pilot_admin",
                                        "-d", DB, "-c", "SELECT token FROM public.pilot_identity",
                                        capture=True).stdout.strip()
                        if actual != token:
                            raise RuntimeError("refusing to remove a database without this run's marker")
                    volumes = docker("inspect", "-f",
                                     "{{range .Mounts}}{{if .Name}}{{println .Name}}{{end}}{{end}}",
                                     container_id, capture=True).stdout.split()
                    docker("rm", "-f", "-v", container_id, check=True, capture=True)
                    remaining = docker("inspect", container_id, check=False, capture=True)
                    if remaining.returncode == 0:
                        raise RuntimeError("owned Docker container remains after cleanup")
                    for volume in volumes:
                        remaining_volume = docker("volume", "inspect", volume, check=False, capture=True)
                        if remaining_volume.returncode == 0:
                            raise RuntimeError(f"owned anonymous Docker volume remains: {volume}")
                except (subprocess.CalledProcessError, OSError, RuntimeError) as cleanup_error:
                    if failure is not None:
                        failure.add_note(f"cleanup also failed: {cleanup_error}")
                    else:
                        raise RuntimeError(f"disposable PostgreSQL cleanup failed: {cleanup_error}") from cleanup_error
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
