#!/usr/bin/env bash
set -euo pipefail
pilot=$(cd "$(dirname "$0")/.." && pwd)
cd "$pilot/../.."
scratch=$(mktemp -d)
container=""
cleanup() {
    if [[ -n "$container" ]]; then docker stop -t 1 "$container" >/dev/null; fi
    rm -rf "$scratch"
}
trap cleanup EXIT
container=$(docker run --rm -d --tmpfs /var/lib/postgresql/data -p 127.0.0.1::5432 \
    -e POSTGRES_DB=personal_baseline_fixture -e POSTGRES_PASSWORD=synthetic-only postgres:16-alpine \
    -c timezone=Europe/Belgrade)
for _ in {1..100}; do
    if docker exec "$container" pg_isready -h 127.0.0.1 >/dev/null; then break; fi
    sleep .2
done
port=$(docker port "$container" 5432/tcp)
[[ "$port" =~ ^127\.0\.0\.1:([0-9]+)$ ]]
port=${BASH_REMATCH[1]}
docker exec -i "$container" psql -Xq -U postgres -d personal_baseline_fixture -v ON_ERROR_STOP=1 < "$pilot/fixtures/personal_baseline.sql"
cat > "$scratch/profiles.yml" <<YAML
health_dbt_metrics_pilot:
  target: fixture
  outputs:
    fixture:
      type: postgres
      host: 127.0.0.1
      port: $port
      user: postgres
      password: synthetic-only
      dbname: personal_baseline_fixture
      schema: derived
      threads: 1
YAML
DBT_SEND_ANONYMOUS_USAGE_STATS=false "$pilot/.venv/bin/dbt" build --project-dir "$pilot" --profiles-dir "$scratch" \
    --target-path "$scratch/target" --log-path "$scratch/log" --vars '{personal_baseline_shadow: true}' --select personal_baseline_v1 hrv_fixed_window_baseline_v1
PERSONAL_BASELINE_FIXTURE_PORT="$port" go test ./internal/storage -count=1 -v -run '^TestPersonalBaselineDBTShadow$'
if [[ -n ${HEALTH_METRIC_AUDIT_DIR:-} ]]; then
    PERSONAL_BASELINE_FIXTURE_PORT="$port" go test ./internal/storage -count=1 -run '^TestMetricRuntimeBaselineCapture$'
    "$pilot/.venv/bin/python" "$pilot/scripts/capture_bound_metrics.py" "$port" baseline
fi
