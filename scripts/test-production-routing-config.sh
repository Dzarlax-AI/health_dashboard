#!/bin/sh
set -eu

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
compose=$root/deploy/production/health/docker-compose.yml
resolver=$root/scripts/resolve-production-pair.sh
tmp=$(mktemp -d "${TMPDIR:-/tmp}/health-production-routing.XXXXXX")
trap 'rm -rf "$tmp"' EXIT HUP INT TERM

backend_digest=sha256:$(printf 'b%.0s' $(seq 1 64))
frontend_digest=sha256:$(printf 'f%.0s' $(seq 1 64))
pair_digest=sha256:$(printf 'a%.0s' $(seq 1 64))
pair_revision=$(printf '1%.0s' $(seq 1 40))
backend_revision=$(printf '2%.0s' $(seq 1 40))
frontend_revision=$(printf '3%.0s' $(seq 1 40))

fake=$tmp/docker
: > "$tmp/runtime.env"
cat > "$fake" <<'EOF'
#!/bin/sh
set -eu
if [ "$1" = pull ]; then exit 0; fi
[ "$1" = image ] && [ "$2" = inspect ] && [ "$3" = --format ]
format=$4
target=$5
case "$format:$target" in
  *RepoDigests*:*pair:compatible) echo "ghcr.io/dzarlax-ai/health_dashboard-pair@$FAKE_PAIR_DIGEST" ;;
  *Architecture*:*pair:compatible) echo amd64 ;;
  *Architecture*:*@*) echo amd64 ;;
  *image-role*:*pair:compatible) echo compatibility-pair ;;
  *pair-revision*:*pair:compatible) echo "$FAKE_PAIR_REVISION" ;;
  *api-contract-version*:*pair:compatible) echo 1.0.0 ;;
  *backend-image*:*pair:compatible) echo "${FAKE_BACKEND_IMAGE:-ghcr.io/dzarlax-ai/health_dashboard}" ;;
  *backend-digest*:*pair:compatible) echo "$FAKE_BACKEND_DIGEST" ;;
  *backend-revision*:*pair:compatible) echo "$FAKE_BACKEND_REVISION" ;;
  *frontend-image*:*pair:compatible) echo ghcr.io/dzarlax-ai/health_dashboard-frontend ;;
  *frontend-digest*:*pair:compatible) echo "$FAKE_FRONTEND_DIGEST" ;;
  *frontend-revision*:*pair:compatible) echo "$FAKE_FRONTEND_REVISION" ;;
  *image-role*:*health_dashboard@*) echo backend ;;
  *image-role*:*health_dashboard-frontend@*) echo frontend ;;
  *revision*:*health_dashboard@*) echo "$FAKE_BACKEND_REVISION" ;;
  *revision*:*health_dashboard-frontend@*) echo "${FAKE_ACTUAL_FRONTEND_REVISION:-$FAKE_FRONTEND_REVISION}" ;;
  *api-contract-version*:*@*) echo 1.0.0 ;;
  *) echo "unexpected fake docker request: $format $target" >&2; exit 1 ;;
esac
EOF
chmod +x "$fake"

export DOCKER_BIN=$fake
export FAKE_PAIR_DIGEST=$pair_digest
export FAKE_PAIR_REVISION=$pair_revision
export FAKE_BACKEND_DIGEST=$backend_digest
export FAKE_FRONTEND_DIGEST=$frontend_digest
export FAKE_BACKEND_REVISION=$backend_revision
export FAKE_FRONTEND_REVISION=$frontend_revision

assert_config() {
  mode=$1
  release=$tmp/$mode.env
  rendered=$tmp/$mode.yml
  gate_rendered=$tmp/$mode-gate.yml
  gate_digest=sha256:$(printf '9%.0s' $(seq 1 64))
  gate_image=ghcr.io/dzarlax-ai/health_dashboard@$gate_digest
  "$resolver" --mode "$mode" --host health.example.com --output "$release"
  HEALTH_RUNTIME_ENV_FILE="$tmp/runtime.env" docker compose --env-file "$release" -f "$compose" config --format json > "$rendered"
  python3 - "$mode" "$rendered" <<'PY'
import json, sys

mode, path = sys.argv[1:]
with open(path, encoding="utf-8") as stream:
    config = json.load(stream)
services = config["services"]
assert set(services) == {"health-receiver", "health-frontend"}
backend = services["health-receiver"]
frontend = services["health-frontend"]
assert backend["image"].startswith("ghcr.io/dzarlax-ai/health_dashboard@sha256:")
assert frontend["image"].startswith("ghcr.io/dzarlax-ai/health_dashboard-frontend@sha256:")
assert set(backend["networks"]) == {"infra", "traefik"}
assert set(frontend["networks"]) == {"traefik"}
b = backend["labels"]
f = frontend["labels"]
assert b["traefik.http.routers.health-machine.rule"].endswith("(PathPrefix(`/health`) || PathPrefix(`/mcp`))")
assert "middlewares" not in {
    key.rsplit(".", 1)[-1]: value
    for key, value in b.items()
    if key.startswith("traefik.http.routers.health-machine.")
}
assert b["traefik.http.routers.health-browser-api.middlewares"] == "authentik-auth"
assert "HeaderRegexp(`X-API-Key`, `.+`)" in b["traefik.http.routers.health-api-key.rule"]
assert "middlewares" not in {
    key.rsplit(".", 1)[-1]: value
    for key, value in b.items()
    if key.startswith("traefik.http.routers.health-api-key.")
}
assert b["traefik.http.routers.health-telegram-webhook.rule"].endswith(
    "PathPrefix(`/api/telegram/webhook/`)"
)
assert int(b["traefik.http.routers.health-telegram-webhook.priority"]) > int(
    b["traefik.http.routers.health-api-key.priority"]
)
assert "middlewares" not in {
    key.rsplit(".", 1)[-1]: value
    for key, value in b.items()
    if key.startswith("traefik.http.routers.health-telegram-webhook.")
}
assert b["traefik.http.routers.health-legacy-root.middlewares"] == "authentik-auth,health-legacy-root"
assert b["traefik.http.middlewares.health-legacy-root.replacepath.path"] == "/"
root = f["traefik.http.routers.health-frontend-root.rule"]
assets = f["traefik.http.routers.health-frontend-assets.rule"]
if mode == "canary":
    assert f["traefik.enable"] == "true"
    assert "HeaderRegexp(`Cookie`" in root and "health_frontend_canary=1" in root
    assert "HeaderRegexp(`Cookie`" in assets and "health_frontend_canary=1" in assets
elif mode == "cutover":
    assert f["traefik.enable"] == "true"
    assert root == "Host(`health.example.com`) && (Path(`/`) || Path(`/sleep`) || Path(`/activity`) || Path(`/cardio`) || Path(`/recovery`))"
    assert assets == "Host(`health.example.com`) && PathPrefix(`/assets/`)"
else:
    assert f["traefik.enable"] == "false"
    assert "__frontend_disabled__" in root
PY
  HEALTH_IMAGE="$gate_image" HEALTH_RUNTIME_ENV_FILE="$tmp/runtime.env" \
    docker compose --env-file "$release" -f "$compose" config --format json > "$gate_rendered"
  python3 - "$gate_image" "$gate_rendered" <<'PY'
import json, sys

expected, path = sys.argv[1:]
with open(path, encoding="utf-8") as stream:
    config = json.load(stream)
actual = config["services"]["health-receiver"]["image"]
assert actual == expected, f"schema gate image override = {actual!r}, want {expected!r}"
PY
}

assert_config canary
assert_config cutover
assert_config rollback

if "$resolver" --mode invalid --host health.example.com --output "$tmp/invalid.env" >/dev/null 2>&1; then
  echo "invalid mode unexpectedly succeeded" >&2
  exit 1
fi
if FAKE_FRONTEND_DIGEST=mutable "$resolver" --mode canary --host health.example.com --output "$tmp/bad.env" >/dev/null 2>&1; then
  echo "invalid digest unexpectedly succeeded" >&2
  exit 1
fi
if FAKE_BACKEND_IMAGE=ghcr.io/example/other "$resolver" --mode canary --host health.example.com --output "$tmp/wrong-image.env" >/dev/null 2>&1; then
  echo "unexpected backend repository unexpectedly succeeded" >&2
  exit 1
fi
if FAKE_ACTUAL_FRONTEND_REVISION=$backend_revision "$resolver" --mode canary --host health.example.com --output "$tmp/mismatch.env" >/dev/null 2>&1; then
  echo "mismatched frontend revision unexpectedly succeeded" >&2
  exit 1
fi
if "$resolver" --mode canary --host 'bad host' --output "$tmp/bad-host.env" >/dev/null 2>&1; then
  echo "invalid host unexpectedly succeeded" >&2
  exit 1
fi

echo "production routing config tests passed"
