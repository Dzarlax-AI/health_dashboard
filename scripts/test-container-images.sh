#!/bin/sh
set -eu

backend_image="${BACKEND_IMAGE:-health-backend:local}"
frontend_image="${FRONTEND_IMAGE:-health-frontend:local}"
build_revision="${BUILD_REVISION:-unknown}"
backend_build_revision="${BACKEND_BUILD_REVISION:-$build_revision}"
frontend_build_revision="${FRONTEND_BUILD_REVISION:-$build_revision}"
image_version="${IMAGE_VERSION:-dev}"
api_contract_version="${API_CONTRACT_VERSION:-unknown}"
frontend_container="health-frontend-contract-$$"

cleanup() {
  docker rm -f "$frontend_container" >/dev/null 2>&1 || true
}
trap cleanup EXIT INT TERM

label_value() {
  docker image inspect --format "{{ index .Config.Labels \"$2\" }}" "$1"
}

assert_label() {
  actual="$(label_value "$1" "$2")"
  if [ "$actual" != "$3" ]; then
    echo "$1 label $2: expected '$3', got '$actual'" >&2
    exit 1
  fi
}

for image in "$backend_image" "$frontend_image"; do
  assert_label "$image" org.opencontainers.image.version "$image_version"
  assert_label "$image" io.health-dashboard.api-contract-version "$api_contract_version"
  docker image inspect --format '{{json .Config.Healthcheck.Test}}' "$image" | grep -q '/healthz'
done

assert_label "$backend_image" org.opencontainers.image.revision "$backend_build_revision"
assert_label "$frontend_image" org.opencontainers.image.revision "$frontend_build_revision"
assert_label "$backend_image" io.health-dashboard.image-role backend
assert_label "$frontend_image" io.health-dashboard.image-role frontend

docker run --rm --entrypoint /bin/sh "$backend_image" -ec '
  test "$(id -u)" -ne 0
  test -x /app/server
  test -x /app/tenant_isolation
  command -v wget >/dev/null 2>&1
'

docker run --rm --entrypoint /bin/sh "$frontend_image" -ec '
  test "$(id -u)" -ne 0
  test ! -e /workspace
  test ! -e /usr/share/nginx/html/package.json
  test ! -e /usr/share/nginx/html/pnpm-lock.yaml
  ! command -v node >/dev/null 2>&1
  ! command -v pnpm >/dev/null 2>&1
  ! find /usr/share/nginx/html -type f \( -name "*.html" -o -name "*.css" \) \
    -exec grep -H -E "https?://" {} +
  ! grep -R -F "Keep the effort controlled and leave some reserve." /usr/share/nginx/html
'

frontend_env="$(docker image inspect --format '{{json .Config.Env}}' "$frontend_image")"
if printf '%s' "$frontend_env" | grep -Eiq 'DATABASE_URL|PGPASSWORD|API_KEY|TENANT_DB|UI_PASSWORD|SETUP_TOKEN'; then
  echo "frontend runtime contains a forbidden secret-like environment variable" >&2
  exit 1
fi

docker run -d --rm --name "$frontend_container" \
  --health-interval=1s --health-timeout=2s --health-retries=5 \
  -p 127.0.0.1::8080 "$frontend_image" >/dev/null

frontend_address=""
i=0
while [ "$i" -lt 30 ]; do
  frontend_address="$(docker port "$frontend_container" 8080/tcp 2>/dev/null | sed -n '1p')"
  health_status="$(docker inspect --format '{{.State.Health.Status}}' "$frontend_container" 2>/dev/null || true)"
  if [ -n "$frontend_address" ] &&
    [ "$health_status" = "healthy" ] &&
    curl --fail --silent "http://$frontend_address/healthz" >/dev/null 2>&1; then
    break
  fi
  i=$((i + 1))
  sleep 1
done

test -n "$frontend_address"
test "$(curl --fail --silent "http://$frontend_address/healthz")" = '{"status":"ok"}'

index_html="$(curl --fail --silent "http://$frontend_address/")"
printf '%s' "$index_html" | grep -q 'id="root"'
asset_path="$(printf '%s' "$index_html" | sed -n 's/.*src="\([^"]*\/assets\/[^"]*\)".*/\1/p' | sed -n '1p')"
test -n "$asset_path"

curl --fail --silent --head "http://$frontend_address/" |
  tr -d '\r' |
  grep -Eiq '^Cache-Control: .*no-cache'
curl --fail --silent --head "http://$frontend_address$asset_path" |
  tr -d '\r' |
  grep -Eiq '^Cache-Control: public, max-age=31536000, immutable$'
test "$(curl --silent --output /dev/null --write-out '%{http_code}' "http://$frontend_address/missing-route")" = "404"

echo "container image contracts verified"
