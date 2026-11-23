#!/usr/bin/env bash
# Own a fresh PostgreSQL cluster and expose only synthetic data to dbt.
set -euo pipefail

root=$(cd "$(dirname "$0")/../../.." && pwd)
pilot="$root/experiments/dbt_metrics"
pg_bin=${SLEEP_REFERENCE_PG_BIN:-$(pg_config --bindir)}
dbt=${SLEEP_REFERENCE_DBT:-"$pilot/.venv/bin/dbt"}
lab=$(mktemp -d /tmp/health-sleep-parity.XXXXXXXX)
container=
cleanup() {
    if [[ -n "$container" ]]; then
        docker stop -t 1 "$container" >/dev/null
    elif [[ -f "$lab/db/postmaster.pid" ]]; then
        "$pg_bin/pg_ctl" -D "$lab/db" -m fast stop >/dev/null
    fi
    rm -rf "$lab"
}
trap cleanup EXIT
if [[ ${1:-} == --docker ]]; then
    container=$(docker run --rm -d --tmpfs /var/lib/postgresql/data -p 127.0.0.1::5432 \
        -e POSTGRES_DB=sleep_reference_lab -e POSTGRES_USER=synthetic_admin \
        -e POSTGRES_PASSWORD=synthetic-only postgres:16-alpine)
    mapping=$(docker port "$container" 5432/tcp)
    [[ "$mapping" =~ ^127\.0\.0\.1:([0-9]+)$ ]]
    port=${BASH_REMATCH[1]}
    host=127.0.0.1
    export PGPASSWORD=synthetic-only
    for _ in {1..100}; do
        if "$pg_bin/pg_isready" -h "$host" -p "$port" -U synthetic_admin -d sleep_reference_lab >/dev/null; then break; fi
        sleep 0.2
    done
else
    host="$lab"
    port=$(python3 - <<'PY'
import socket
with socket.socket() as sock:
    sock.bind(("127.0.0.1", 0))
    print(sock.getsockname()[1])
PY
)
    "$pg_bin/initdb" -D "$lab/db" -U synthetic_admin -A trust --no-locale -E UTF8 >/dev/null
    "$pg_bin/pg_ctl" -D "$lab/db" -l "$lab/postgres.log" -o "-k $lab -p $port -h 127.0.0.1" -w start
    "$pg_bin/createdb" -h "$lab" -p "$port" -U synthetic_admin sleep_reference_lab
fi
marker=$(basename "$lab")
"$pg_bin/psql" -X -h "$host" -p "$port" -U synthetic_admin -d sleep_reference_lab -v ON_ERROR_STOP=1 -v marker="$marker" <<'SQL'
create table public.synthetic_sleep_reference(token text not null);
insert into public.synthetic_sleep_reference values (:'marker');
alter database sleep_reference_lab set timezone='Europe/Belgrade';
create role synthetic_dbt login password 'synthetic-only';
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
    claim_eligibility text not null,
    observed_at timestamptz not null default '2026-10-26 08:00:00+00',
    diagnostics jsonb not null default '{}'
);
create table source_data.settings(key text primary key, value text not null);
insert into source_data.settings values ('sleep_duration_comparison_enabled','true');
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
    --vars '{"sleep_duration_reference": true, "semantic_shadow": true}' \
    --indirect-selection cautious --select sleep_duration_reference_v1 semantic_sleep_duration_comparison_v1

cd "$root"
export SLEEP_REFERENCE_PILOT_PORT="$port" SLEEP_REFERENCE_PILOT_MARKER="$marker"
export GOCACHE="${GOCACHE:-$lab/go-cache}"
go test ./internal/storage -run '^TestSleepComparisonActualDBTParity$' -count=1 -v
go test ./experiments/dbt_metrics -run '^TestSleepReferenceParity$' -count=1 -v

export HEALTH_DB_TESTS=1 READINESS_TEST_DSN="host=$host port=$port user=synthetic_admin dbname=sleep_reference_lab sslmode=disable"
go test ./cmd/server -run '^TestTodaySleepComparisonBriefing$' -count=1 -v
go test ./internal/notify -run '^TestMorningDeliveryWaitsForCurrentAIAndDoesNotWaitForCheckin$' -count=1 -v

if [[ -n ${HEALTH_METRIC_AUDIT_DIR:-} ]]; then
    go test ./internal/storage -run '^Test(MetricSchema|NamedContract)' -count=1 -v
fi
