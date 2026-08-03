#!/bin/sh
set -eu

backend_image="${BACKEND_IMAGE:-health-backend:local}"
frontend_image="${FRONTEND_IMAGE:-health-frontend:local}"
build_revision="${BUILD_REVISION:-unknown}"
api_contract_version="${API_CONTRACT_VERSION:-unknown}"
postgres_image="${POSTGRES_IMAGE:-postgres:17-alpine@sha256:742f40ea20b9ff2ff31db5458d127452988a2164df9e17441e191f3b72252193}"
proxy_image="${PROXY_IMAGE:-nginxinc/nginx-unprivileged:1.31.3-alpine3.24@sha256:a6c3ec0c0d249d68b0682df854d4a9e222b90fb607dc3fcf2f1d2fcbc85d347e}"

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
proxy_config="$script_dir/fixtures/compatible-image-pair.nginx.conf"
pair_prefix="${PAIR_PREFIX:-}"
network_name="${PAIR_NETWORK:-}"
db_container="${PAIR_DB_CONTAINER:-}"
backend_container="${PAIR_BACKEND_CONTAINER:-}"
frontend_container="${PAIR_FRONTEND_CONTAINER:-}"
proxy_container="${PAIR_PROXY_CONTAINER:-}"
proxy_port="${PAIR_PROXY_PORT:-}"
run_token="${PAIR_RUN_TOKEN:-}"
owner_label="io.health-dashboard.compatibility-run"

network_created=0
db_created=0
backend_created=0
frontend_created=0
proxy_created=0
tmp_dir=""

root_password="__root_password_not_generated__"
admin_password="__admin_password_not_generated__"
registry_password="__registry_password_not_generated__"
master_secret="__master_secret_not_generated__"
api_key="__api_key_not_generated__"
ui_password="__ui_password_not_generated__"

sanitize_logs() {
  sed \
    -e "s|$root_password|[REDACTED]|g" \
    -e "s|$admin_password|[REDACTED]|g" \
    -e "s|$registry_password|[REDACTED]|g" \
    -e "s|$master_secret|[REDACTED]|g" \
    -e "s|$api_key|[REDACTED]|g" \
    -e "s|$ui_password|[REDACTED]|g" \
    -e 's|postgres://[^[:space:]]*|postgres://[REDACTED]|g'
}

print_failure_logs() {
  print_container_logs "$proxy_container"
  print_container_logs "$backend_container"
  print_container_logs "$frontend_container"
  print_container_logs "$db_container"
}

print_container_logs() {
  container="$1"
  if owned_container "$container"; then
    echo "----- logs: $container -----" >&2
    docker logs "$container" 2>&1 | sanitize_logs >&2 || true
  fi
}

owned_container() {
  container="$1"
  [ -n "$container" ] &&
    [ -n "$run_token" ] &&
    [ "$(docker container inspect --format "{{ index .Config.Labels \"$owner_label\" }}" "$container" 2>/dev/null || true)" = "$run_token" ]
}

owned_network() {
  [ -n "$network_name" ] &&
    [ -n "$run_token" ] &&
    [ "$(docker network inspect --format "{{ index .Labels \"$owner_label\" }}" "$network_name" 2>/dev/null || true)" = "$run_token" ]
}

remove_owned_container() {
  container="$1"
  created="$2"
  if owned_container "$container"; then
    docker rm -f -v "$container" >/dev/null 2>&1 || true
  elif [ "$created" -eq 1 ]; then
    echo "compatible image pair: refusing to remove '$container' because its ownership label changed" >&2
  fi
}

cleanup() {
  status=$?
  trap - 0
  if [ "$status" -ne 0 ]; then
    print_failure_logs
  fi
  remove_owned_container "$proxy_container" "$proxy_created"
  remove_owned_container "$frontend_container" "$frontend_created"
  remove_owned_container "$backend_container" "$backend_created"
  remove_owned_container "$db_container" "$db_created"
  if owned_network; then
    docker network rm "$network_name" >/dev/null 2>&1 || true
  elif [ "$network_created" -eq 1 ]; then
    echo "compatible image pair: refusing to remove '$network_name' because its ownership label changed" >&2
  fi
  if [ -n "$tmp_dir" ] && [ -d "$tmp_dir" ]; then rm -rf "$tmp_dir"; fi
  exit "$status"
}

handle_signal() {
  signal_status="$1"
  trap - 1 2 15
  exit "$signal_status"
}

trap cleanup 0
trap 'handle_signal 130' 2
trap 'handle_signal 143' 15

case "${PAIR_SELF_TEST_SIGNAL:-}" in
  INT) kill -INT "$$" ;;
  TERM) kill -TERM "$$" ;;
  "") ;;
  *) echo "compatible image pair: PAIR_SELF_TEST_SIGNAL must be INT or TERM" >&2; exit 2 ;;
esac

fail() {
  echo "compatible image pair: $*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command '$1' was not found"
}

validate_name() {
  case "$2" in
    ""|*[!abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.-]*)
      fail "$1 must contain only letters, digits, dots, underscores, or hyphens"
      ;;
  esac
}

label_value() {
  if ! docker image inspect --format "{{ index .Config.Labels \"$2\" }}" "$1" 2>/dev/null; then
    fail "cannot inspect image '$1'; build or pull it before running this harness"
  fi
}

assert_label() {
  actual="$(label_value "$1" "$2")"
  if [ "$actual" != "$3" ]; then
    fail "image '$1' label '$2' expected '$3', got '$actual'; rebuild both images from the same revision and API contract"
  fi
}

wait_for_http() {
  url="$1"
  attempts="$2"
  i=0
  while [ "$i" -lt "$attempts" ]; do
    if curl --fail --silent --show-error "$url" >/dev/null 2>&1; then
      return 0
    fi
    i=$((i + 1))
    sleep 1
  done
  return 1
}

require_command docker

# Compatibility is checked before any network, container, or route probe exists.
assert_label "$backend_image" io.health-dashboard.image-role backend
assert_label "$frontend_image" io.health-dashboard.image-role frontend
assert_label "$backend_image" org.opencontainers.image.revision "$build_revision"
assert_label "$frontend_image" org.opencontainers.image.revision "$build_revision"
assert_label "$backend_image" io.health-dashboard.api-contract-version "$api_contract_version"
assert_label "$frontend_image" io.health-dashboard.api-contract-version "$api_contract_version"

if [ "${PAIR_PREFLIGHT_ONLY:-0}" = "1" ]; then
  echo "compatible image pair preflight verified"
  exit 0
fi

require_command curl
require_command openssl
require_command python3
test -f "$proxy_config" || fail "proxy fixture not found at '$proxy_config'"

if [ -z "$run_token" ]; then
  run_token="$(openssl rand -hex 16)"
fi
validate_name PAIR_RUN_TOKEN "$run_token"
if [ -z "$pair_prefix" ]; then pair_prefix="health-pair-$run_token"; fi
if [ -z "$network_name" ]; then network_name="$pair_prefix-net"; fi
if [ -z "$db_container" ]; then db_container="$pair_prefix-db"; fi
if [ -z "$backend_container" ]; then backend_container="$pair_prefix-backend"; fi
if [ -z "$frontend_container" ]; then frontend_container="$pair_prefix-frontend"; fi
if [ -z "$proxy_container" ]; then proxy_container="$pair_prefix-proxy"; fi

validate_name PAIR_NETWORK "$network_name"
validate_name PAIR_DB_CONTAINER "$db_container"
validate_name PAIR_BACKEND_CONTAINER "$backend_container"
validate_name PAIR_FRONTEND_CONTAINER "$frontend_container"
validate_name PAIR_PROXY_CONTAINER "$proxy_container"
case "$proxy_port" in
  ""|*[!0123456789]*) [ -z "$proxy_port" ] || fail "PAIR_PROXY_PORT must be numeric" ;;
esac

for container in "$db_container" "$backend_container" "$frontend_container" "$proxy_container"; do
  if docker container inspect "$container" >/dev/null 2>&1; then
    fail "container '$container' already exists; choose a unique PAIR_PREFIX"
  fi
done
if docker network inspect "$network_name" >/dev/null 2>&1; then
  fail "network '$network_name' already exists; choose a unique PAIR_PREFIX"
fi

tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/health-compatible-pair.XXXXXX")"
chmod 0700 "$tmp_dir"
manifest_dir="$tmp_dir/identity"
cookie_jar="$tmp_dir/session.cookies"
mkdir "$manifest_dir"
chmod 0700 "$manifest_dir"
touch "$cookie_jar"
chmod 0600 "$cookie_jar"

root_password="$(openssl rand -hex 32)"
admin_password="$(openssl rand -hex 32)"
registry_password="$(openssl rand -hex 32)"
master_secret="$(openssl rand -base64 32 | tr -d '\n')"
api_key="$(openssl rand -hex 32)"
ui_password="$(openssl rand -hex 32)"

docker network create --label "$owner_label=$run_token" "$network_name" >/dev/null
network_created=1

if [ "${PAIR_SELF_TEST_CLEANUP_FAILURE:-0}" = "1" ]; then
  fail "intentional post-create failure for cleanup verification"
fi

docker run -d --name "$db_container" --network "$network_name" --network-alias database \
  --label "$owner_label=$run_token" \
  -e POSTGRES_DB=health -e POSTGRES_USER=postgres -e POSTGRES_PASSWORD="$root_password" \
  "$postgres_image" >/dev/null
db_created=1

docker run --rm --network "$network_name" -e PGPASSWORD="$root_password" \
  "$postgres_image" /bin/sh -ec '
    i=0
    while [ "$i" -lt 60 ]; do
      if [ "$(psql -h database -U postgres -d health -Atc "SELECT 1" 2>/dev/null)" = "1" ]; then
        exit 0
      fi
      i=$((i + 1))
      sleep 1
    done
    exit 1
  '

manifest=/bootstrap/database-identity-manifest.json
docker run --rm --user 0:0 --network "$network_name" -v "$manifest_dir:/bootstrap" \
  -e TENANT_DB_BOOTSTRAP_DATABASE_URL="postgres://postgres:${root_password}@database:5432/health?sslmode=disable" \
  -e HEALTH_ADMIN_DB_PASSWORD="$admin_password" \
  -e HEALTH_REGISTRY_DB_PASSWORD="$registry_password" \
  --entrypoint /app/tenant_isolation "$backend_image" \
  --mode bootstrap-db-identities --manifest "$manifest" --confirm

docker run --rm --user 0:0 -v "$manifest_dir:/bootstrap:ro" \
  --entrypoint /bin/sh "$backend_image" -ec '
    test -f /bootstrap/database-identity-manifest.json
    test "$(stat -c "%u" /bootstrap/database-identity-manifest.json)" = "0"
    test "$(stat -c "%a" /bootstrap/database-identity-manifest.json)" = "600"
  '

docker run -d --name "$backend_container" --network "$network_name" --network-alias backend \
  --label "$owner_label=$run_token" \
  -e TENANT_DB_ISOLATION_ENABLED=true \
  -e ADMIN_DATABASE_URL="postgres://health_admin:${admin_password}@database:5432/health" \
  -e REGISTRY_DATABASE_URL="postgres://health_registry:${registry_password}@database:5432/health" \
  -e TENANT_DATABASE_URL_BASE='postgres://database:5432/health?sslmode=disable' \
  -e TENANT_DB_MASTER_SECRET="$master_secret" \
  -e TENANT_DB_MASTER_SECRET_VERSION=1 \
  -e API_KEY="$api_key" \
  -e UI_PASSWORD="$ui_password" \
  "$backend_image" >/dev/null
backend_created=1

docker exec "$backend_container" /bin/sh -ec '
  test -z "$TENANT_DB_BOOTSTRAP_DATABASE_URL"
  case "$ADMIN_DATABASE_URL $REGISTRY_DATABASE_URL" in
    *postgres://postgres:*) exit 1 ;;
  esac
'

i=0
while [ "$i" -lt 90 ]; do
  if docker exec "$backend_container" wget -q -O - http://127.0.0.1:8080/readyz >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
docker exec "$backend_container" wget -q -O - http://127.0.0.1:8080/readyz >/dev/null

for mode in migrate-contract audit; do
  confirm_args=""
  if [ "$mode" = "migrate-contract" ]; then confirm_args="--confirm"; fi
  docker run --rm --network "$network_name" \
    -e TENANT_DB_ISOLATION_ENABLED=true \
    -e ADMIN_DATABASE_URL="postgres://health_admin:${admin_password}@database:5432/health" \
    -e REGISTRY_DATABASE_URL="postgres://health_registry:${registry_password}@database:5432/health" \
    -e TENANT_DATABASE_URL_BASE='postgres://database:5432/health?sslmode=disable' \
    -e TENANT_DB_MASTER_SECRET="$master_secret" \
    -e TENANT_DB_MASTER_SECRET_VERSION=1 \
    --entrypoint /app/tenant_isolation "$backend_image" \
    --mode "$mode" --all --primary-schema health $confirm_args
done

docker run --rm --user 0:0 --network "$network_name" -v "$manifest_dir:/bootstrap" \
  -e TENANT_DB_BOOTSTRAP_DATABASE_URL="postgres://postgres:${root_password}@database:5432/health" \
  --entrypoint /app/tenant_isolation "$backend_image" \
  --mode finalize-db-identities --manifest "$manifest" --confirm
docker run --rm --network "$network_name" \
  -e ADMIN_DATABASE_URL="postgres://health_admin:${admin_password}@database:5432/health" \
  -e REGISTRY_DATABASE_URL="postgres://health_registry:${registry_password}@database:5432/health" \
  --entrypoint /app/tenant_isolation "$backend_image" \
  --mode verify-db-identities

docker run -d --name "$frontend_container" --network "$network_name" --network-alias frontend \
  --label "$owner_label=$run_token" \
  "$frontend_image" >/dev/null
frontend_created=1

docker exec "$frontend_container" wget -q -O - http://127.0.0.1:8080/healthz >/dev/null
docker exec "$backend_container" wget -q -O - http://127.0.0.1:8080/readyz >/dev/null

if [ -n "$proxy_port" ]; then
  publish_arg="127.0.0.1:${proxy_port}:8080"
else
  publish_arg="127.0.0.1::8080"
fi
docker run -d --name "$proxy_container" --network "$network_name" \
  --label "$owner_label=$run_token" \
  -p "$publish_arg" \
  -v "$proxy_config:/etc/nginx/conf.d/default.conf:ro" \
  "$proxy_image" >/dev/null
proxy_created=1

proxy_binding=""
i=0
while [ "$i" -lt 60 ]; do
  proxy_binding="$(docker port "$proxy_container" 8080/tcp 2>/dev/null | sed -n '1p' | sed 's/.*://')"
  if [ -n "$proxy_binding" ] && wait_for_http "http://127.0.0.1:$proxy_binding/" 1; then
    break
  fi
  i=$((i + 1))
  sleep 1
done
[ -n "$proxy_binding" ] || fail "proxy did not publish a host port"
proxy_url="http://127.0.0.1:$proxy_binding"

spa_html="$tmp_dir/spa.html"
curl --fail --silent --show-error "$proxy_url/" -o "$spa_html"
grep -q 'id="root"' "$spa_html" || fail "routed / did not return the frontend SPA shell"
asset_path="$(sed -n 's/.*src="\([^"]*\/assets\/[^"]*\)".*/\1/p' "$spa_html" | sed -n '1p')"
[ -n "$asset_path" ] || fail "routed SPA shell did not reference a frontend asset"
curl --fail --silent --show-error "$proxy_url$asset_path" -o /dev/null ||
  fail "routed frontend asset namespace did not serve the SPA bundle"

login_status="$(
  curl --silent --show-error --output "$tmp_dir/login.html" --dump-header "$tmp_dir/login.headers" \
    --cookie-jar "$cookie_jar" --write-out '%{http_code}' \
    --request POST "$proxy_url/login?next=/" \
    --header 'Content-Type: application/x-www-form-urlencoded' \
    --data-urlencode 'username=admin' \
    --data-urlencode "password=$ui_password"
)"
[ "$login_status" = "302" ] || fail "routed password login expected HTTP 302, got $login_status"

dashboard_status="$(
  curl --silent --show-error --output "$tmp_dir/dashboard.json" --write-out '%{http_code}' \
    --cookie "$cookie_jar" "$proxy_url/api/dashboard?lang=en"
)"
[ "$dashboard_status" = "200" ] || fail "authenticated routed /api/dashboard expected HTTP 200, got $dashboard_status"
python3 -c 'import json,sys; value=json.load(open(sys.argv[1])); assert isinstance(value, dict)' "$tmp_dir/dashboard.json" ||
  fail "authenticated routed /api/dashboard did not return a valid JSON object"

metrics_status="$(
  curl --silent --show-error --output "$tmp_dir/metrics.html" --write-out '%{http_code}' \
    --cookie "$cookie_jar" "$proxy_url/metrics"
)"
[ "$metrics_status" = "200" ] || fail "authenticated routed /metrics expected HTTP 200, got $metrics_status"
grep -Eiq '<!doctype html|<html' "$tmp_dir/metrics.html" ||
  fail "authenticated routed /metrics did not return legacy HTML"

unauth_health_status="$(
  curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
    --request POST "$proxy_url/health" \
    --header 'Content-Type: application/json' \
    --data '{"data":{"metrics":[]}}'
)"
[ "$unauth_health_status" = "401" ] ||
  fail "unauthenticated routed POST /health expected HTTP 401, got $unauth_health_status"

unauth_mcp_status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' "$proxy_url/mcp")"
[ "$unauth_mcp_status" = "401" ] ||
  fail "unauthenticated routed /mcp expected HTTP 401, got $unauth_mcp_status"

auth_health_status="$(
  curl --silent --show-error --output /dev/null --write-out '%{http_code}' \
    --request POST "$proxy_url/health" \
    --header "X-API-Key: $api_key" \
    --header 'Content-Type: application/json' \
    --data '{"data":{"metrics":[]}}'
)"
[ "$auth_health_status" = "200" ] ||
  fail "authorized routed POST /health expected HTTP 200, got $auth_health_status; verify proxy header forwarding"

echo "compatible frontend/backend image pair verified"
