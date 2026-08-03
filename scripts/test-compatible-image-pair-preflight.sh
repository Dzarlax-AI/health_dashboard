#!/bin/sh
set -eu

backend_image="${BACKEND_IMAGE:-health-backend:local}"
frontend_image="${FRONTEND_IMAGE:-health-frontend:local}"
build_revision="${BUILD_REVISION:-unknown}"
api_contract_version="${API_CONTRACT_VERSION:-unknown}"

script_dir="$(CDPATH= cd "$(dirname "$0")" && pwd)"
harness="$script_dir/test-compatible-image-pair.sh"
fixture="$script_dir/fixtures/contract-mismatch.Dockerfile"
token="$(openssl rand -hex 12)"
test_prefix="health-pair-preflight-$token"
backend_mismatch="health-pair-backend-mismatch:$token"
frontend_mismatch="health-pair-frontend-mismatch:$token"
foreign_network="$test_prefix-foreign-net"
tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/health-pair-preflight.XXXXXX")"
backend_built=0
frontend_built=0
foreign_network_created=0

cleanup() {
  status=$?
  trap - 0
  if [ "$status" -ne 0 ]; then
    for output in "$tmp_dir"/*.out; do
      if [ -f "$output" ]; then
        echo "----- $(basename "$output") -----" >&2
        sed -e 's|postgres://[^[:space:]]*|postgres://[REDACTED]|g' "$output" >&2
      fi
    done
  fi
  if [ "$foreign_network_created" -eq 1 ]; then
    docker network rm "$foreign_network" >/dev/null 2>&1 || true
  fi
  if [ "$frontend_built" -eq 1 ]; then docker image rm -f "$frontend_mismatch" >/dev/null 2>&1 || true; fi
  if [ "$backend_built" -eq 1 ]; then docker image rm -f "$backend_mismatch" >/dev/null 2>&1 || true; fi
  rm -rf "$tmp_dir"
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

fail() {
  echo "compatible image pair preflight tests: $*" >&2
  exit 1
}

assert_no_pair_resources() {
  prefix="$1"
  for suffix in net db backend frontend proxy; do
    case "$suffix" in
      net)
        if docker network inspect "$prefix-net" >/dev/null 2>&1; then
          fail "preflight unexpectedly created network '$prefix-net'"
        fi
        ;;
      *)
        if docker container inspect "$prefix-$suffix" >/dev/null 2>&1; then
          fail "preflight unexpectedly created container '$prefix-$suffix'"
        fi
        ;;
    esac
  done
  return 0
}

assert_contract_rejected() {
  name="$1"
  backend="$2"
  frontend="$3"
  prefix="$test_prefix-$name"
  output="$tmp_dir/$name.out"
  if BACKEND_IMAGE="$backend" \
    FRONTEND_IMAGE="$frontend" \
    BUILD_REVISION="$build_revision" \
    API_CONTRACT_VERSION="$api_contract_version" \
    PAIR_PREFIX="$prefix" \
    PAIR_PREFLIGHT_ONLY=1 \
    "$harness" >"$output" 2>&1; then
    fail "$name contract mismatch unexpectedly passed"
  fi
  grep -q "rebuild both images from the same revision and API contract" "$output" ||
    fail "$name mismatch did not produce the actionable compatibility error"
  assert_no_pair_resources "$prefix"
}

assert_signal_status() {
  signal="$1"
  expected="$2"
  output="$tmp_dir/signal-$signal.out"
  status=0
  PAIR_SELF_TEST_SIGNAL="$signal" "$harness" >"$output" 2>&1 || status=$?
  [ "$status" -eq "$expected" ] ||
    fail "$signal expected exit $expected, got $status"
}

command -v docker >/dev/null 2>&1 || fail "docker is required"
command -v openssl >/dev/null 2>&1 || fail "openssl is required"
test -x "$harness" || fail "harness is not executable"
test -f "$fixture" || fail "contract mismatch fixture is missing"

docker build --quiet -f "$fixture" \
  --build-arg BASE_IMAGE="$backend_image" \
  --build-arg API_CONTRACT_VERSION="$api_contract_version-deliberately-wrong" \
  -t "$backend_mismatch" "$script_dir/fixtures" >/dev/null
backend_built=1
echo "derived backend mismatch image built"
docker build --quiet -f "$fixture" \
  --build-arg BASE_IMAGE="$frontend_image" \
  --build-arg API_CONTRACT_VERSION="$api_contract_version-deliberately-wrong" \
  -t "$frontend_mismatch" "$script_dir/fixtures" >/dev/null
frontend_built=1
echo "derived frontend mismatch image built"

# Each side is mismatched independently. Removing either comparison from the
# harness makes its corresponding assertion unexpectedly pass.
assert_contract_rejected backend "$backend_mismatch" "$frontend_image"
echo "backend contract mismatch rejected"
assert_contract_rejected frontend "$backend_image" "$frontend_mismatch"
echo "frontend contract mismatch rejected"

assert_signal_status INT 130
assert_signal_status TERM 143
echo "SIGINT and SIGTERM exit statuses verified"

cleanup_prefix="$test_prefix-cleanup"
cleanup_output="$tmp_dir/cleanup-failure.out"
cleanup_status=0
BACKEND_IMAGE="$backend_image" \
  FRONTEND_IMAGE="$frontend_image" \
  BUILD_REVISION="$build_revision" \
  API_CONTRACT_VERSION="$api_contract_version" \
  PAIR_PREFIX="$cleanup_prefix" \
  PAIR_SELF_TEST_CLEANUP_FAILURE=1 \
  "$harness" >"$cleanup_output" 2>&1 || cleanup_status=$?
[ "$cleanup_status" -eq 1 ] || fail "post-create cleanup self-test expected exit 1, got $cleanup_status"
assert_no_pair_resources "$cleanup_prefix"
echo "post-create failure cleanup verified"

# A colliding resource without this run's ownership token must survive.
docker network create \
  --label io.health-dashboard.compatibility-run=foreign \
  "$foreign_network" >/dev/null
foreign_network_created=1
collision_status=0
BACKEND_IMAGE="$backend_image" \
  FRONTEND_IMAGE="$frontend_image" \
  BUILD_REVISION="$build_revision" \
  API_CONTRACT_VERSION="$api_contract_version" \
  PAIR_NETWORK="$foreign_network" \
  PAIR_RUN_TOKEN="$token" \
  "$harness" >"$tmp_dir/foreign-collision.out" 2>&1 || collision_status=$?
[ "$collision_status" -eq 1 ] || fail "foreign network collision expected exit 1, got $collision_status"
docker network inspect "$foreign_network" >/dev/null 2>&1 ||
  fail "harness removed a network owned by another run"
echo "foreign resource ownership protected"

echo "compatible image pair preflight, signal, and cleanup tests passed"
