#!/bin/sh
set -eu

root=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
compose=$root/deploy/production/health/docker-compose.yml
traefik_image=${TRAEFIK_IMAGE:-traefik:v3.6}
tmp=$(mktemp -d "${TMPDIR:-/tmp}/health-traefik-rules.XXXXXX")
name=health-traefik-rules-$$
owner_label=io.health-dashboard.traefik-rule-test
owner_token=$name

owned_container() {
  [ "$(docker container inspect --format "{{ index .Config.Labels \"$owner_label\" }}" "$name" 2>/dev/null || true)" = "$owner_token" ]
}

remove_owned_container() {
  if owned_container; then
    docker rm -f "$name" >/dev/null 2>&1 || true
  fi
}

cleanup() {
  remove_owned_container
  rm -rf "$tmp"
}
trap cleanup EXIT HUP INT TERM
: > "$tmp/runtime.env"

write_release() {
  mode=$1
  case "$mode" in
    canary)
      root_rule='Host(`health.example.com`) && (Path(`/`) || Path(`/sleep`)) && HeaderRegexp(`Cookie`, `(^|;[ ]*)health_frontend_canary=1(;|[ ]*$)`)'
      assets_rule='Host(`health.example.com`) && PathPrefix(`/assets/`) && HeaderRegexp(`Cookie`, `(^|;[ ]*)health_frontend_canary=1(;|[ ]*$)`)'
      ;;
    cutover)
      root_rule='Host(`health.example.com`) && (Path(`/`) || Path(`/sleep`))'
      assets_rule='Host(`health.example.com`) && PathPrefix(`/assets/`)'
      ;;
    *) exit 2 ;;
  esac
  {
    echo "HEALTH_BACKEND_IMAGE=example.invalid/backend@sha256:$(printf 'b%.0s' $(seq 1 64))"
    echo "HEALTH_FRONTEND_IMAGE=example.invalid/frontend@sha256:$(printf 'f%.0s' $(seq 1 64))"
    echo "HEALTH_HOST=health.example.com"
    echo "HEALTH_FRONTEND_TRAEFIK_ENABLED=true"
    printf "HEALTH_FRONTEND_ROOT_RULE='%s'\n" "$root_rule"
    printf "HEALTH_FRONTEND_ASSETS_RULE='%s'\n" "$assets_rule"
  } > "$tmp/release.env"
}

render_dynamic_config() {
  mode=$1
  write_release "$mode"
  HEALTH_RUNTIME_ENV_FILE="$tmp/runtime.env" docker compose \
    --env-file "$tmp/release.env" -f "$compose" config --format json > "$tmp/compose.json"
  python3 - "$tmp/compose.json" "$tmp/dynamic.yml" <<'PY'
import json, sys

source, target = sys.argv[1:]
with open(source, encoding="utf-8") as stream:
    compose = json.load(stream)

routers = {}
for service in compose["services"].values():
    labels = service.get("labels", {})
    if labels.get("traefik.enable") != "true":
        continue
    prefix = "traefik.http.routers."
    names = {key[len(prefix):].split(".", 1)[0] for key in labels if key.startswith(prefix)}
    for name in names:
        base = f"{prefix}{name}."
        router = {
            "rule": labels[base + "rule"],
            "service": labels[base + "service"],
            "entryPoints": ["https"],
            "tls": {},
        }
        middleware = labels.get(base + "middlewares")
        if middleware:
            router["middlewares"] = middleware.split(",")
        routers[name] = router

dynamic = {
    "http": {
        "routers": routers,
        "middlewares": {
            "authentik-auth": {
                "forwardAuth": {
                    "address": "http://127.0.0.1:9999/auth",
                    "trustForwardHeader": True,
                }
            },
            "health-legacy-root": {"replacePath": {"path": "/"}},
        },
        "services": {
            "health-backend": {"loadBalancer": {"servers": [{"url": "http://127.0.0.1:9001"}]}},
            "health-frontend": {"loadBalancer": {"servers": [{"url": "http://127.0.0.1:9002"}]}},
        },
    }
}
with open(target, "w", encoding="utf-8") as stream:
    json.dump(dynamic, stream)
PY
}

docker pull "$traefik_image" >/dev/null
for mode in canary cutover; do
  render_dynamic_config "$mode"
  if docker container inspect "$name" >/dev/null 2>&1; then
    owned_container || {
      echo "refusing to replace unrelated container $name" >&2
      exit 1
    }
    remove_owned_container
  fi
  docker run -d --name "$name" \
    --label "$owner_label=$owner_token" \
    -p 127.0.0.1::8080 \
    -v "$tmp/dynamic.yml:/dynamic.yml:ro" \
    "$traefik_image" \
    --api.insecure=true \
    --entrypoints.https.address=:8443 \
    --providers.file.filename=/dynamic.yml >/dev/null

  address=$(docker port "$name" 8080/tcp | tail -n 1)
  attempt=0
  until curl --fail --silent "http://$address/api/rawdata" > "$tmp/rawdata.json" &&
    python3 - "$tmp/rawdata.json" <<'PY'
import json, sys
with open(sys.argv[1], encoding="utf-8") as stream:
    raw = json.load(stream)
raise SystemExit(0 if len(raw.get("routers", {})) >= 7 else 1)
PY
  do
    attempt=$((attempt + 1))
    [ "$attempt" -lt 30 ] || {
      docker logs "$name" >&2
      echo "Traefik API did not become ready for $mode" >&2
      exit 1
    }
    sleep 1
  done
  python3 - "$mode" "$tmp/rawdata.json" <<'PY'
import json, sys

mode, path = sys.argv[1:]
with open(path, encoding="utf-8") as stream:
    raw = json.load(stream)
errors = []
for kind in ("routers", "middlewares", "services"):
    for name, item in raw.get(kind, {}).items():
        errors.extend(f"{kind}.{name}: {error}" for error in item.get("errors", []))
if errors:
    raise SystemExit(f"{mode} Traefik configuration errors:\n" + "\n".join(errors))
expected = {
    "health-machine@file",
    "health-api-key@file",
    "health-telegram-webhook@file",
    "health-browser-api@file",
    "health-legacy@file",
    "health-legacy-root@file",
    "health-frontend-root@file",
    "health-frontend-assets@file",
}
missing = expected.difference(raw.get("routers", {}))
if missing:
    raise SystemExit(
        f"{mode} missing enabled routers: {sorted(missing)}; "
        f"loaded={sorted(raw.get('routers', {}))}"
    )
PY
  remove_owned_container
done

echo "production Traefik rule tests passed"
