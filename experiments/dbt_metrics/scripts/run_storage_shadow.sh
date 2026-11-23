#!/usr/bin/env bash
# Run a storage/dbt parity lane in its own disposable PostgreSQL database.
set -euo pipefail
cd "$(dirname "$0")/../../.."
case "${1:-}" in
    energy)
        fixture=energy_capture
        models=(semantic_energy_bank_v3 semantic_energy_guidance_v1 semantic_energy_legacy_v1)
        config='{energy_bank_shadow: true}'
        tests='^TestEnergy((Bank|Guidance|Legacy)DBTParity|MissingRHRBriefing|ObservedReserveWithoutLegacyVerdict)$|^TestMetricRuntimeEnergyCapture$'
        guard=ENERGY_BANK_PILOT
        ;;
    workouts)
        fixture=workout_capture
        models=(workout_period_v1 workout_type_v1)
        config='{workout_shadow: true}'
        tests='^Test(WorkoutDBTParity|MetricRuntimeWorkoutCapture)$'
        guard=WORKOUT_STATS_PILOT
        ;;
    *) exit 2 ;;
esac
pilot=experiments/dbt_metrics
lab=$(mktemp -d "${TMPDIR:-/tmp}/storage-dbt-shadow.XXXXXXXX")
container=
cleanup() {
    if [[ -n "$container" ]]; then docker rm -fv "$container" >/dev/null; fi
    rm -rf -- "$lab"
}
trap cleanup EXIT
container=$(docker run -d --rm -p 127.0.0.1::5432 --tmpfs /var/lib/postgresql/data \
    -e POSTGRES_DB=storage_pilot -e POSTGRES_USER=pilot_admin \
    -e POSTGRES_PASSWORD=synthetic-only postgres:16-alpine)
for _ in {1..60}; do
    if docker exec "$container" pg_isready -h 127.0.0.1 -U pilot_admin -d storage_pilot >/dev/null; then break; fi
    sleep 0.25
done
port=$(docker port "$container" 5432/tcp)
[[ "$port" == 127.0.0.1:* ]]
for schema_file in schema "$fixture"; do
    docker exec -i "$container" psql -Xq -v ON_ERROR_STOP=1 -U pilot_admin -d storage_pilot < "$pilot/fixtures/$schema_file.sql"
done
cat > "$lab/profiles.yml" <<PROFILE
health_dbt_metrics_pilot:
  target: local
  outputs:
    local:
      type: postgres
      host: 127.0.0.1
      port: ${port##*:}
      user: pilot_dbt
      password: synthetic-only
      dbname: storage_pilot
      schema: dbt_output
      threads: 1
PROFILE
dbt=( "$pilot/.venv/bin/dbt" )
args=(--project-dir "$pilot" --profiles-dir "$lab" --target-path "$lab/target" --log-path "$lab/logs"
      --vars "$config" --select "${models[@]}")
export DBT_SEND_ANONYMOUS_USAGE_STATS=false
"${dbt[@]}" run "${args[@]}"
env "$guard=synthetic-only" HEALTH_DB_TESTS=1 READINESS_TEST_DSN="postgres://pilot_admin:synthetic-only@${port}/storage_pilot?sslmode=disable" \
    go test ./internal/storage -run "$tests" -count=1 -v
"${dbt[@]}" test "${args[@]}"
if [[ -n "${HEALTH_METRIC_AUDIT_DIR:-}" ]]; then
    "$pilot/.venv/bin/python" "$pilot/scripts/capture_bound_metrics.py" "${port##*:}" "$1"
fi
