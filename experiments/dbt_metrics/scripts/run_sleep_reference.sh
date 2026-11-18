#!/usr/bin/env bash
# Own a fresh PostgreSQL cluster and expose only synthetic data to dbt.
set -euo pipefail

root=$(cd "$(dirname "$0")/../../.." && pwd)
pilot="$root/experiments/dbt_metrics"
pg_bin=${SLEEP_REFERENCE_PG_BIN:-$(pg_config --bindir)}
dbt=${SLEEP_REFERENCE_DBT:-"$pilot/.venv/bin/dbt"}
lab=$(mktemp -d /tmp/health-sleep-parity.XXXXXXXX)
trap '"$pg_bin/pg_ctl" -D "$lab/db" -m fast stop >/dev/null 2>&1 || true; rm -rf "$lab"' EXIT
port=$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)
marker=$(basename "$lab")
"$pg_bin/initdb" -D "$lab/db" -U synthetic_admin -A trust --no-locale -E UTF8 >/dev/null
"$pg_bin/pg_ctl" -D "$lab/db" -l "$lab/postgres.log" -o "-k $lab -p $port -h 127.0.0.1" -w start
"$pg_bin/createdb" -h "$lab" -p "$port" -U synthetic_admin sleep_reference_lab
"$pg_bin/psql" -X -h "$lab" -p "$port" -U synthetic_admin -d sleep_reference_lab -v ON_ERROR_STOP=1 -v marker="$marker" <<'SQL'
create table public.synthetic_sleep_reference(token text not null);
insert into public.synthetic_sleep_reference values (:'marker');
alter database sleep_reference_lab set timezone='Europe/Belgrade';
create role synthetic_dbt login;
create schema dbt_output authorization synthetic_dbt;
create schema source_data;
create table source_data.completed_night_sleep(
    wake_date text primary key,
    duration_hours double precision not null,
    source text not null,
    source_epoch text not null,
    algorithm_version text not null,
    input_hash text not null,
    capture_completeness text not null,
    duration_assessment text not null,
    finalization_state text not null,
    claim_eligibility text not null
);
grant usage on schema source_data to synthetic_dbt;
grant select on source_data.completed_night_sleep to synthetic_dbt;
SQL

cat > "$lab/profiles.yml" <<PROFILE
health_dbt_metrics_pilot:
  target: local
  outputs:
    local:
      type: postgres
      host: 127.0.0.1
      port: $port
      user: synthetic_dbt
      password: synthetic-only
      dbname: sleep_reference_lab
      schema: dbt_output
      threads: 1
PROFILE
DBT_SEND_ANONYMOUS_USAGE_STATS=false "$dbt" build --project-dir "$pilot" --profiles-dir "$lab" \
    --target-path "$lab/target" --log-path "$lab/logs" \
    --vars '{"sleep_duration_reference": true}' --select sleep_duration_reference_v1

cd "$root"
SLEEP_REFERENCE_PILOT_PORT="$port" SLEEP_REFERENCE_PILOT_MARKER="$marker" \
    GOCACHE="${GOCACHE:-$lab/go-cache}" go test ./experiments/dbt_metrics -run '^TestSleepReferenceParity$' -count=1 -v
