#!/bin/sh
set -eu

pair_ref=ghcr.io/dzarlax-ai/health_dashboard-pair:compatible
expected_pair_image=ghcr.io/dzarlax-ai/health_dashboard-pair
expected_backend_image=ghcr.io/dzarlax-ai/health_dashboard
expected_frontend_image=ghcr.io/dzarlax-ai/health_dashboard-frontend
mode=
host=
output=
docker_bin=${DOCKER_BIN:-docker}

fail() {
  echo "production pair resolver: $*" >&2
  exit 1
}

usage() {
  echo "usage: $0 --mode canary|cutover|rollback --host HOST --output FILE [--pair IMAGE:TAG]" >&2
  exit 2
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --mode) [ "$#" -ge 2 ] || usage; mode=$2; shift 2 ;;
    --host) [ "$#" -ge 2 ] || usage; host=$2; shift 2 ;;
    --output) [ "$#" -ge 2 ] || usage; output=$2; shift 2 ;;
    --pair) [ "$#" -ge 2 ] || usage; pair_ref=$2; shift 2 ;;
    *) usage ;;
  esac
done

case "$mode" in canary|cutover|rollback) ;; *) usage ;; esac
[ -n "$output" ] || usage
[ -n "$host" ] || usage
command -v "$docker_bin" >/dev/null 2>&1 || fail "docker was not found"
command -v python3 >/dev/null 2>&1 || fail "python3 was not found"

pair_ref=$(printf '%s' "$pair_ref" | tr '[:upper:]' '[:lower:]')
host=$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]')
case "$host" in
  *..*|.*|*.) fail "--host must be a valid DNS hostname" ;;
esac
printf '%s\n' "$host" | grep -Eq '^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$' ||
  fail "--host must be a valid DNS hostname"
[ "${#host}" -le 253 ] || fail "--host must be at most 253 characters"
case "$pair_ref" in *@sha256:*) fail "--pair must name the movable compatible pointer, not a digest" ;; esac
case "$pair_ref" in *:compatible) ;; *) fail "--pair must end in :compatible" ;; esac
pair_image=${pair_ref%:compatible}
[ "$pair_image" = "$expected_pair_image" ] || fail "--pair must use the authoritative health_dashboard pair repository"

label() {
  "$docker_bin" image inspect --format "{{ index .Config.Labels \"$1\" }}" "$2"
}

valid_digest() {
  printf '%s\n' "$1" | grep -Eq '^sha256:[0-9a-f]{64}$'
}

valid_revision() {
  printf '%s\n' "$1" | grep -Eq '^[0-9a-f]{40}$'
}

valid_contract() {
  case "$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')" in
    ""|"<no value>"|n/a|none|null|unknown|unset) return 1 ;;
  esac
  printf '%s\n' "$1" | grep -Eq '^[A-Za-z0-9][A-Za-z0-9._-]*$'
}

repo_digest() {
  repository=$1
  target=$2
  "$docker_bin" image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$target" |
    awk -v prefix="$repository@sha256:" 'index($0, prefix) == 1 { print substr($0, index($0, "@") + 1); exit }'
}

"$docker_bin" pull --platform linux/amd64 "$pair_ref" >/dev/null
pair_role=$(label io.health-dashboard.image-role "$pair_ref")
pair_revision=$(label io.health-dashboard.pair-revision "$pair_ref")
contract=$(label io.health-dashboard.api-contract-version "$pair_ref")
backend_image=$(label io.health-dashboard.backend-image "$pair_ref" | tr '[:upper:]' '[:lower:]')
backend_digest=$(label io.health-dashboard.backend-digest "$pair_ref")
backend_revision=$(label io.health-dashboard.backend-revision "$pair_ref")
frontend_image=$(label io.health-dashboard.frontend-image "$pair_ref" | tr '[:upper:]' '[:lower:]')
frontend_digest=$(label io.health-dashboard.frontend-digest "$pair_ref")
frontend_revision=$(label io.health-dashboard.frontend-revision "$pair_ref")

[ "$pair_role" = compatibility-pair ] || fail "compatible pointer has the wrong image role"
[ "$backend_image" = "$expected_backend_image" ] || fail "compatible pointer selected an unexpected backend image"
[ "$frontend_image" = "$expected_frontend_image" ] || fail "compatible pointer selected an unexpected frontend image"
valid_revision "$pair_revision" || fail "pair revision is missing or invalid"
valid_revision "$backend_revision" || fail "backend revision is missing or invalid"
valid_revision "$frontend_revision" || fail "frontend revision is missing or invalid"
valid_digest "$backend_digest" || fail "backend digest is missing or invalid"
valid_digest "$frontend_digest" || fail "frontend digest is missing or invalid"
valid_contract "$contract" || fail "API contract version is missing or invalid"
[ -n "$backend_image" ] && [ -n "$frontend_image" ] || fail "component image name is missing"

pair_digest=$(repo_digest "$pair_image" "$pair_ref")
valid_digest "$pair_digest" || fail "could not resolve immutable compatibility-pair digest"
pair_architecture=$("$docker_bin" image inspect --format '{{.Architecture}}' "$pair_ref")
[ "$pair_architecture" = amd64 ] || fail "compatibility-pair architecture must be amd64"

backend_ref=$backend_image@$backend_digest
frontend_ref=$frontend_image@$frontend_digest
"$docker_bin" pull --platform linux/amd64 "$backend_ref" >/dev/null
"$docker_bin" pull --platform linux/amd64 "$frontend_ref" >/dev/null

verify_component() {
  ref=$1
  expected_role=$2
  expected_revision=$3
  actual_role=$(label io.health-dashboard.image-role "$ref")
  actual_revision=$(label org.opencontainers.image.revision "$ref")
  actual_contract=$(label io.health-dashboard.api-contract-version "$ref")
  architecture=$("$docker_bin" image inspect --format '{{.Architecture}}' "$ref")
  [ "$actual_role" = "$expected_role" ] || fail "$expected_role image role mismatch"
  [ "$actual_revision" = "$expected_revision" ] || fail "$expected_role revision mismatch"
  [ "$actual_contract" = "$contract" ] || fail "$expected_role API contract mismatch"
  [ "$architecture" = amd64 ] || fail "$expected_role architecture must be amd64"
}

verify_component "$backend_ref" backend "$backend_revision"
verify_component "$frontend_ref" frontend "$frontend_revision"

case "$mode" in
  canary)
    enabled=true
    root_rule="Host(\`$host\`) && Path(\`/\`) && HeaderRegexp(\`Cookie\`, \`(^|;[ ]*)health_frontend_canary=1(;|[ ]*\$)\`)"
    assets_rule="Host(\`$host\`) && PathPrefix(\`/assets/\`) && HeaderRegexp(\`Cookie\`, \`(^|;[ ]*)health_frontend_canary=1(;|[ ]*\$)\`)"
    ;;
  cutover)
    enabled=true
    root_rule="Host(\`$host\`) && Path(\`/\`)"
    assets_rule="Host(\`$host\`) && PathPrefix(\`/assets/\`)"
    ;;
  rollback)
    enabled=false
    root_rule="Host(\`$host\`) && Path(\`/__frontend_disabled__\`)"
    assets_rule="Host(\`$host\`) && PathPrefix(\`/__frontend_disabled__/assets/\`)"
    ;;
esac

output_dir=$(dirname "$output")
[ -d "$output_dir" ] || fail "output directory does not exist: $output_dir"
tmp=$(mktemp "$output_dir/.release.env.XXXXXX")
trap 'rm -f "$tmp"' EXIT HUP INT TERM
chmod 0600 "$tmp"
{
  printf 'HEALTH_PAIR_IMAGE=%s@%s\n' "$pair_image" "$pair_digest"
  printf 'HEALTH_PAIR_REVISION=%s\n' "$pair_revision"
  printf 'HEALTH_API_CONTRACT_VERSION=%s\n' "$contract"
  printf 'HEALTH_BACKEND_IMAGE=%s\n' "$backend_ref"
  printf 'HEALTH_BACKEND_REVISION=%s\n' "$backend_revision"
  printf 'HEALTH_FRONTEND_IMAGE=%s\n' "$frontend_ref"
  printf 'HEALTH_FRONTEND_REVISION=%s\n' "$frontend_revision"
  printf 'HEALTH_HOST=%s\n' "$host"
  printf 'HEALTH_FRONTEND_ROUTE_MODE=%s\n' "$mode"
  printf 'HEALTH_FRONTEND_TRAEFIK_ENABLED=%s\n' "$enabled"
  printf "HEALTH_FRONTEND_ROOT_RULE='%s'\n" "$root_rule"
  printf "HEALTH_FRONTEND_ASSETS_RULE='%s'\n" "$assets_rule"
} > "$tmp"
mv "$tmp" "$output"
trap - EXIT HUP INT TERM
echo "production pair resolver: wrote verified $mode release to $output"
