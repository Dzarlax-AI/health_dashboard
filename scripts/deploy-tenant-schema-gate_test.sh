#!/bin/bash
set -euo pipefail

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
production_gate=$script_dir/deploy-tenant-schema-gate.sh
tmp=$(mktemp -d "${TMPDIR:-/tmp}/health-schema-gate-test.XXXXXX")
tmp=$(CDPATH= cd -- "$tmp" && pwd -P)
trap 'rm -rf "$tmp"' EXIT
mkdir -p "$tmp/bin" "$tmp/compose" "$tmp/gate-tmp"
chmod 0755 "$tmp/bin" "$tmp/compose" "$tmp/gate-tmp"
printf '%s\n' 'services:' '  health-receiver:' '    image: ${HEALTH_IMAGE}' '    env_file:' '      - dependency.env' >"$tmp/compose/compose.yml"
printf 'ORIGINAL=1\n' >"$tmp/compose/dependency.env"
printf 'TENANT_DB_MASTER_SECRET=do-not-leak-this-secret\n' >"$tmp/audit.env"
chmod 600 "$tmp/audit.env"

# The production entrypoint must reject this rootless test process before doing
# anything else. Functional tests use a generated copy with only literal trust
# ownership/anchor constants relaxed; production has no environment bypass.
nonroot_output=$tmp/nonroot.out
if "$production_gate" --help >"$nonroot_output" 2>&1; then
  printf 'FAIL: production gate accepted a non-root caller\n' >&2; exit 1
fi
grep -q 'must run as root' "$nonroot_output" || { printf 'FAIL: non-root rejection missing\n' >&2; exit 1; }

uid=$(id -u)
test_gate=$tmp/deploy-tenant-schema-gate-test-copy.sh
/usr/bin/python3 -c '
import sys
src,dst,fake,temp,uid=sys.argv[1:]
s=open(src,encoding="utf-8").read()
replacements=[
 ("required_euid=0",f"required_euid={uid}"),
 ("trusted_uid=0",f"trusted_uid={uid}"),
 ("allow_untrusted_ancestors_for_tests=0","allow_untrusted_ancestors_for_tests=1"),
 ("trusted_path=\x27/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\x27",f"trusted_path=\x27{fake}:/usr/bin:/bin\x27"),
 ("temp_base=/var/tmp",f"temp_base=\x27{temp}\x27"),
 ("stability_interval=1","stability_interval=0.01"),
]
for old,new in replacements:
 if s.count(old) != 1: raise SystemExit(f"test transform expected exactly one tagged constant: {old}")
 s=s.replace(old,new,1)
open(dst,"w",encoding="utf-8").write(s)
' "$production_gate" "$test_gate" "$tmp/bin" "$tmp/gate-tmp" "$uid"
chmod 0755 "$test_gate"
grep -Fq "trusted_path='$tmp/bin:/usr/bin:/bin'" "$test_gate" || { printf 'FAIL: generated test PATH is not the controlled portable fixture PATH\n' >&2; exit 1; }
grep -Fq "trusted_path='/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'" "$production_gate" || { printf 'FAIL: production trusted PATH changed\n' >&2; exit 1; }

digest_a='ghcr.io/example/health@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
digest_d='ghcr.io/example/health@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd'
unrelated='ghcr.io/example/other@sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee'
mutable='ghcr.io/example/health:latest'
previous_id='sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc'
command_log=$tmp/commands.log
state_dir=$tmp/state
mkdir -p "$state_dir"

cat >"$tmp/bin/docker" <<'EOF'
#!/bin/bash
set -euo pipefail
printf 'HEALTH_IMAGE=%s docker' "${HEALTH_IMAGE:-}" >>"$FAKE_COMMAND_LOG"
printf ' <%s>' "$@" >>"$FAKE_COMMAND_LOG"
printf '\n' >>"$FAKE_COMMAND_LOG"
if [[ $1 == compose && $2 == version ]]; then exit 0; fi
if [[ $1 == pull ]]; then exit 0; fi
if [[ $1 == image && $2 == inspect ]]; then
  if [[ $4 == '{{.Id}}' ]]; then printf 'sha256:9999999999999999999999999999999999999999999999999999999999999999\n'; else printf '%b\n' "$FAKE_REPO_DIGESTS"; fi
  exit 0
fi
if [[ $1 == network && $2 == inspect ]]; then
  [[ ${FAKE_NETWORK_MISSING:-0} != 1 ]] || exit 1
  printf '[{"Name":"%s"}]\n' "$3"; exit 0
fi
if [[ $1 == compose ]]; then
  action=''; compose_file=''
  for ((i=1; i<=$#; i++)); do
    case ${!i} in
      -f) j=$((i+1)); compose_file=${!j} ;;
      config|ps|stop|up) action=${!i}; break ;;
    esac
  done
  case "$action" in
    config)
      if [[ $compose_file == *health-schema-recovery.*/* ]]; then
        /usr/bin/python3 -c '
import json,os,sys
value=json.load(open(sys.argv[1])); target=value["services"]["health-receiver"]
if os.environ.get("FAKE_RECOVERY_DRIFT")=="1": target["image"]="sha256:"+"f"*64
if os.environ.get("FAKE_RECOVERY_BUILD")=="1": target["build"]={"context":"."}
print(json.dumps(value,separators=(",",":")))
' "$compose_file"
        exit 0
      fi
      image=${HEALTH_IMAGE:-}
      [[ ${FAKE_COMPOSE_IGNORES_IMAGE:-0} != 1 ]] || image='ghcr.io/example/health:previous'
      if [[ ${FAKE_RECOVERY_RICH:-0} == 1 ]]; then
        printf '{"services":{"health-receiver":{"image":"%s","pull_policy":"always","depends_on":{"unrelated":{"condition":"service_started"}},"networks":{"app":null},"volumes":[{"type":"volume","source":"health-data","target":"/data"}],"secrets":[{"source":"health-secret","target":"health-secret"}]},"unrelated":{"image":"busybox","environment":{"UNRELATED_SECRET":"must-not-persist"}}},"networks":{"app":{"name":"health-app"},"unused":{"name":"unused"}},"volumes":{"health-data":{"name":"health-data"},"unused-data":{"name":"unused-data"}},"secrets":{"health-secret":{"file":"/private/health-secret"},"unrelated-secret":{"environment":"UNRELATED_SECRET"}}}\n' "$image"
      elif [[ ${FAKE_COMPOSE_BUILD_OVERRIDE:-0} == 1 ]]; then
        printf '{"services":{"health-receiver":{"image":"%s","build":{"context":"."}}}}\n' "$image"
      elif [[ ${FAKE_COMPOSE_PULL_ALWAYS:-0} == 1 ]]; then
        printf '{"services":{"health-receiver":{"image":"%s","pull_policy":"always"}}}\n' "$image"
      else
        printf '{"services":{"health-receiver":{"image":"%s"}}}\n' "$image"
      fi
      if [[ ${FAKE_COMPOSE_SWAP_DURING_RENDER:-0} == 1 ]]; then printf '# render swap\n' >>"$FAKE_COMPOSE_FILE"; fi ;;
    ps)
      [[ $compose_file != "$FAKE_COMPOSE_FILE" ]] || { printf 'original compose reused\n' >&2; exit 30; }
      /usr/bin/python3 -c 'import json,os,stat,sys; s=os.stat(sys.argv[1]); v=json.load(open(sys.argv[1])); assert stat.S_IMODE(s.st_mode)==0o600 and v["services"]["health-receiver"]["image"]==sys.argv[2]' "$compose_file" "$FAKE_TARGET_DIGEST"
      if [[ ${FAKE_MUTATE_SOURCE_AFTER_RENDER:-0} == 1 && ! -f $FAKE_STATE_DIR/source-mutated ]]; then
        printf '# later source mutation\n' >>"$FAKE_COMPOSE_FILE"; printf 'MUTATED=1\n' >"$FAKE_DEPENDENCY_FILE"; touch "$FAKE_STATE_DIR/source-mutated"
      fi
      if [[ -f $FAKE_STATE_DIR/started ]]; then
        n=$(cat "$FAKE_STATE_DIR/runtime-ps" 2>/dev/null || printf 0); n=$((n+1)); printf '%s' "$n" >"$FAKE_STATE_DIR/runtime-ps"
        if [[ ${FAKE_RUNTIME:-} == replacement && $n -ge 2 ]]; then printf 'replacement-container\n'; else printf 'new-container\n'; fi
      else
        printf 'old-container\n'
      fi ;;
    stop|up)
      [[ $compose_file != "$FAKE_COMPOSE_FILE" ]] || exit 30
      /usr/bin/python3 -c 'import json,sys; assert json.load(open(sys.argv[1]))["services"]["health-receiver"]["image"]==sys.argv[2]' "$compose_file" "$FAKE_TARGET_DIGEST"
      if [[ $action == stop ]]; then touch "$FAKE_STATE_DIR/stopped"; else touch "$FAKE_STATE_DIR/started"; fi ;;
    *) exit 10 ;;
  esac
  exit 0
fi
if [[ $1 == inspect ]]; then
  format=$3; container=$4
  case "$format" in
    '{{.Config.Image}}')
      if [[ $container == old-container ]]; then printf 'ghcr.io/example/health:previous\n'
      elif [[ ${FAKE_RUNTIME:-} == image-mismatch ]]; then printf 'ghcr.io/example/health@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff\n'
      else printf '%s\n' "$FAKE_TARGET_DIGEST"; fi ;;
    '{{.Image}}')
      [[ ${FAKE_FAIL_PREVIOUS_ID:-0} != 1 ]] || { printf 'inspect canary-secret\n' >&2; exit 17; }
      printf '%s\n' "$FAKE_PREVIOUS_ID" ;;
    '{{.State.Running}}') printf 'true\n' ;;
    '{{.RestartCount}}')
      if [[ $container == old-container ]]; then printf '0\n'
      elif [[ ${FAKE_RUNTIME:-} == restart ]]; then printf '1\n'
      elif [[ ${FAKE_RUNTIME:-} == restart-after ]]; then
        n=$(cat "$FAKE_STATE_DIR/restart-inspects" 2>/dev/null || printf 0); n=$((n+1)); printf '%s' "$n" >"$FAKE_STATE_DIR/restart-inspects"
        if ((n > 3)); then printf '1\n'; else printf '0\n'; fi
      else printf '0\n'; fi ;;
    *) exit 9 ;;
  esac
  exit 0
fi
if [[ $1 == run ]]; then
  env_file=''
  mode=''
  for ((i=1; i<=$#; i++)); do
    if [[ ${!i} == --mode ]]; then j=$((i+1)); mode=${!j}; fi
    if [[ ${!i} == --env-file ]]; then j=$((i+1)); env_file=${!j}; fi
  done
  [[ -n $env_file && $env_file != "$FAKE_ORIGINAL_ENV" ]] || exit 40
  /usr/bin/python3 -c 'import os,stat,sys; p=sys.argv[1]; s=os.stat(p); assert stat.S_IMODE(s.st_mode)==0o600 and open(p).read()=="TENANT_DB_MASTER_SECRET=do-not-leak-this-secret\n"' "$env_file"
  if [[ ${FAKE_MUTATE_ORIGINAL_ENV:-0} == 1 && ! -f $FAKE_STATE_DIR/env-mutated ]]; then printf 'BROKEN=changed\n' >"$FAKE_ORIGINAL_ENV"; touch "$FAKE_STATE_DIR/env-mutated"; fi
  if [[ ${FAKE_MALFORMED_ENV_LAUNCH:-0} == 1 && ! -f $FAKE_STATE_DIR/stopped ]]; then printf 'malformed-env-canary\n' >&2; exit 125; fi
  if [[ $mode == migrate-contract ]]; then
    [[ -f $FAKE_STATE_DIR/stopped ]] || exit 20
    if [[ ${FAKE_PAYLOAD:-} == migration-contradiction ]]; then
      printf '{"status":"pass","target_contract_version":1,"target_contract_checksum":"1111111111111111111111111111111111111111111111111111111111111111","attempted":3,"completed":2}\n'; exit 0
    fi
    printf '{"status":"pass","target_contract_version":1,"target_contract_checksum":"1111111111111111111111111111111111111111111111111111111111111111","attempted":3,"completed":3}\n'; exit 0
  fi
  n=1; while [[ -f $FAKE_STATE_DIR/audit-$n ]]; do n=$((n+1)); done; touch "$FAKE_STATE_DIR/audit-$n"
  if [[ ${FAKE_TRANSPORT_LOGICAL_FAIL:-0} == 1 && $n == 1 ]]; then
    printf '{"status":"fail","target_contract_version":1,"target_contract_checksum":"1111111111111111111111111111111111111111111111111111111111111111","counts":{"registry_by_state":{"active":3},"markers":2,"roles":3},"probes":{"attempted":9,"denied":9,"failed":0},"findings":[{"code":"pre_migration_state","scope":"contract"}]}\n'; exit 1
  fi
  if [[ ${FAKE_FAIL_STAGE:-} == preaudit && $n == 2 ]] || [[ ${FAKE_FAIL_STAGE:-} == postaudit && $n == 3 ]]; then
    printf 'docker-run-stderr-canary-secret\n' >&2
    printf '{"status":"fail","target_contract_version":1,"target_contract_checksum":"1111111111111111111111111111111111111111111111111111111111111111","counts":{"registry_by_state":{"active":3},"markers":3,"roles":3},"probes":{"attempted":9,"denied":8,"failed":1},"findings":[{"code":"safe_failure","scope":"fleet"}]}\n'; exit 1
  fi
  if [[ ${FAKE_PAYLOAD:-} == audit-contradiction && $n == 2 ]]; then
    printf '{"status":"pass","target_contract_version":1,"target_contract_checksum":"1111111111111111111111111111111111111111111111111111111111111111","counts":{"registry_by_state":{"active":3},"markers":3,"roles":3},"probes":{"attempted":9,"denied":8,"failed":1},"findings":[]}\n'; exit 0
  fi
  if [[ ${FAKE_PAYLOAD:-} == invalid-version && $n == 2 ]]; then
    printf '{"status":"pass","target_contract_version":0,"target_contract_checksum":"1111111111111111111111111111111111111111111111111111111111111111","counts":{"registry_by_state":{"active":3},"markers":3,"roles":3},"probes":{"attempted":9,"denied":9,"failed":0},"findings":[]}\n'; exit 0
  fi
  if [[ ${FAKE_PAYLOAD:-} == invalid-checksum && $n == 2 ]]; then
    printf '{"status":"pass","target_contract_version":1,"target_contract_checksum":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","counts":{"registry_by_state":{"active":3},"markers":3,"roles":3},"probes":{"attempted":9,"denied":9,"failed":0},"findings":[]}\n'; exit 0
  fi
  version=1; checksum='1111111111111111111111111111111111111111111111111111111111111111'
  if [[ ${FAKE_PAYLOAD:-} == contract-mismatch-pre && $n == 2 ]] || [[ ${FAKE_PAYLOAD:-} == contract-mismatch-post && $n == 3 ]]; then version=2; fi
  if [[ ${FAKE_PAYLOAD:-} == zero-probes && $n == 2 ]]; then
    printf '{"status":"pass","target_contract_version":1,"target_contract_checksum":"1111111111111111111111111111111111111111111111111111111111111111","counts":{"registry_by_state":{"active":3},"markers":3,"roles":3},"probes":{"attempted":0,"denied":0,"failed":0},"findings":[]}\n'; exit 0
  fi
  if [[ ${FAKE_PAYLOAD:-} == marker-mismatch && $n == 2 ]]; then
    printf '{"status":"pass","target_contract_version":1,"target_contract_checksum":"1111111111111111111111111111111111111111111111111111111111111111","counts":{"registry_by_state":{"active":3},"markers":2,"roles":3},"probes":{"attempted":9,"denied":9,"failed":0},"findings":[]}\n'; exit 0
  fi
  printf '{"status":"pass","target_contract_version":%s,"target_contract_checksum":"%s","counts":{"registry_by_state":{"active":3},"markers":3,"roles":3},"probes":{"attempted":9,"denied":9,"failed":0},"findings":[]}\n' "$version" "$checksum"; exit 0
fi
if [[ $1 == exec ]]; then [[ ${FAKE_RUNTIME:-} != timeout ]]; exit; fi
if [[ $1 == logs ]]; then
  if [[ ${FAKE_RUNTIME:-} == fatal ]]; then printf 'level=fatal startup failed\n'; else printf 'normal startup\n'; fi
  exit 0
fi
exit 99
EOF
chmod 0755 "$tmp/bin/docker"

reset_fixture() {
  : >"$command_log"
  rm -f "$state_dir"/*
  printf '%s\n' 'services:' '  health-receiver:' '    image: ${HEALTH_IMAGE}' '    env_file:' '      - dependency.env' >"$tmp/compose/compose.yml"
  printf 'ORIGINAL=1\n' >"$tmp/compose/dependency.env"
  printf 'TENANT_DB_MASTER_SECRET=do-not-leak-this-secret\n' >"$tmp/audit.env"
  chmod 600 "$tmp/audit.env"
}
run_gate() {
  local output=$1 fail=${2:-} runtime=${3:-} payload=${4:-} digests=$digest_a
  (($# < 5)) || digests=$5
  local special=${6:-} ignores=0 network_missing=0 malformed_env=0 render_swap=0 build_override=0 logical_fail=0 hardcoded_compose=0 pull_always=0 recovery_rich=0 recovery_drift=0 recovery_build=0
  case "$special" in
    ignored-image) ignores=1 ;;
    missing-network) network_missing=1 ;;
    malformed-env) malformed_env=1 ;;
    render-swap) render_swap=1 ;;
    build-override) build_override=1 ;;
    transport-logical-fail) logical_fail=1 ;;
    hardcoded-compose) hardcoded_compose=1 ;;
    pull-policy-always) pull_always=1 ;;
    recovery-rich) recovery_rich=1 ;;
    recovery-drift) recovery_drift=1 ;;
    recovery-build) recovery_build=1 ;;
  esac
  local rc=0
  reset_fixture
  if ((hardcoded_compose == 1)); then
    printf '%s\n' 'services:' '  health-receiver:' "    image: $digest_a" '    env_file:' '      - dependency.env' >"$tmp/compose/compose.yml"
  fi
  PATH="$tmp/bin:$PATH" FAKE_COMMAND_LOG="$command_log" FAKE_REPO_DIGESTS="$digests" \
    FAKE_TARGET_DIGEST="$digest_a" FAKE_PREVIOUS_ID="$previous_id" FAKE_STATE_DIR="$state_dir" \
    FAKE_FAIL_STAGE="$fail" FAKE_RUNTIME="$runtime" FAKE_PAYLOAD="$payload" \
    FAKE_COMPOSE_FILE="$tmp/compose/compose.yml" FAKE_DEPENDENCY_FILE="$tmp/compose/dependency.env" \
    FAKE_ORIGINAL_ENV="$tmp/audit.env" FAKE_MUTATE_ORIGINAL_ENV=1 FAKE_MUTATE_SOURCE_AFTER_RENDER=1 \
    FAKE_COMPOSE_IGNORES_IMAGE="$ignores" FAKE_NETWORK_MISSING="$network_missing" \
    FAKE_MALFORMED_ENV_LAUNCH="$malformed_env" FAKE_COMPOSE_SWAP_DURING_RENDER="$render_swap" \
    FAKE_COMPOSE_BUILD_OVERRIDE="$build_override" \
    FAKE_COMPOSE_PULL_ALWAYS="$pull_always" FAKE_RECOVERY_RICH="$recovery_rich" \
    FAKE_RECOVERY_DRIFT="$recovery_drift" FAKE_RECOVERY_BUILD="$recovery_build" \
    FAKE_TRANSPORT_LOGICAL_FAIL="$logical_fail" \
    "$test_gate" --image "$mutable" --compose-dir "$tmp/compose" --compose-file compose.yml \
      --project health-test --service health-receiver --primary-schema health_primary \
      --audit-env "$tmp/audit.env" --network health-net --readiness-timeout 2 >"$output" 2>&1 || rc=$?
  return "$rc"
}
expect_failure() {
  local name=$1 fail=${2:-} runtime=${3:-} payload=${4:-} digests=$digest_a
  (($# < 5)) || digests=$5
  local special=${6:-}
  local output=$tmp/$name.out
  if run_gate "$output" "$fail" "$runtime" "$payload" "$digests" "$special"; then printf 'FAIL: %s returned success\n' "$name" >&2; exit 1; fi
  printf '%s\n' "$output"
}

success=$tmp/success.out
if ! run_gate "$success"; then
  printf 'FAIL: expected success fixture failed\n' >&2
  sed 's/do-not-leak-this-secret/[REDACTED]/g' "$success" >&2
  exit 1
fi
grep -q 'Tenant schema release gate passed' "$success" || { printf 'FAIL: success missing\n' >&2; exit 1; }
[[ $(grep -c '<config> <--format> <json>' "$command_log") == 1 ]] || { printf 'FAIL: effective compose was not rendered exactly once\n' >&2; exit 1; }
[[ $(grep -c 'docker <run>' "$command_log") == 4 ]] || { printf 'FAIL: wrong one-shot count\n' >&2; exit 1; }
[[ $(grep 'docker <run>' "$command_log" | grep -c "<$digest_a>") == 4 ]] || { printf 'FAIL: digest drift\n' >&2; exit 1; }
grep -q "HEALTH_IMAGE=$digest_a docker .*<up>" "$command_log" || { printf 'FAIL: runtime digest not pinned\n' >&2; exit 1; }
line_of() { grep -n -m1 -- "$1" "$command_log" | cut -d: -f1; }
stop_line=$(line_of '<stop> <health-receiver>')
migrate_line=$(line_of '<--mode> <migrate-contract>')
transport_line=$(grep -n -- '<--mode> <audit>' "$command_log" | sed -n '1p' | cut -d: -f1)
preaudit_line=$(grep -n -- '<--mode> <audit>' "$command_log" | sed -n '2p' | cut -d: -f1)
up_line=$(line_of '<up> <-d> <--no-deps> <health-receiver>')
ready_line=$(line_of '<exec> <new-container>')
postaudit_line=$(grep -n -- '<--mode> <audit>' "$command_log" | sed -n '3p' | cut -d: -f1)
logs_line=$(line_of '<logs> <--since>')
((transport_line < stop_line && stop_line < migrate_line && migrate_line < preaudit_line && preaudit_line < up_line && up_line < ready_line && ready_line < postaudit_line && postaudit_line < logs_line)) || { printf 'FAIL: release ordering changed\n' >&2; exit 1; }
resolution_line=$(line_of '<image> <inspect>')
if tail -n "+$((resolution_line + 1))" "$command_log" | grep -q -- "$mutable"; then printf 'FAIL: mutable target reused after resolution\n' >&2; exit 1; fi
if grep 'docker <run>' "$command_log" | grep -q "<$tmp/audit.env>"; then printf 'FAIL: mutable audit env used\n' >&2; exit 1; fi
[[ $(grep -c '<exec> <new-container>' "$command_log") -ge 6 ]] || { printf 'FAIL: consecutive stability checks missing\n' >&2; exit 1; }
grep -q '<logs> <--since> <[0-9].*Z> <new-container>' "$command_log" || { printf 'FAIL: logs are not bounded by deployment timestamp\n' >&2; exit 1; }
[[ -f $state_dir/source-mutated && -f $state_dir/env-mutated ]] || { printf 'FAIL: source/env mutation fixture did not run\n' >&2; exit 1; }
grep -q 'later source mutation' "$tmp/compose/compose.yml" || { printf 'FAIL: compose source was not mutated after render\n' >&2; exit 1; }
grep -q 'BROKEN=changed' "$tmp/audit.env" || { printf 'FAIL: original audit env was not mutated after snapshot\n' >&2; exit 1; }

run_gate "$tmp/transport-logical-fail.out" '' '' '' "$digest_a" transport-logical-fail
grep -q 'Tenant schema release gate passed' "$tmp/transport-logical-fail.out" || { printf 'FAIL: valid logical transport audit failure was not accepted\n' >&2; exit 1; }

preout=$(expect_failure preaudit preaudit)
grep -q 'Reviewed recovery command' "$preout" || { printf 'FAIL: recovery command missing\n' >&2; exit 1; }
grep -q 'Private recovery Compose snapshot:' "$preout" || { printf 'FAIL: private recovery snapshot missing\n' >&2; exit 1; }
recovery_snapshot=$(sed -n 's/^Private recovery Compose snapshot: //p' "$preout")
[[ -f $recovery_snapshot ]] || { printf 'FAIL: recovery snapshot did not survive gate cleanup\n' >&2; exit 1; }
/usr/bin/python3 -c 'import json,os,stat,sys; p,image=sys.argv[1:]; s=os.stat(p); d=os.stat(os.path.dirname(p)); v=json.load(open(p)); target=v["services"]["health-receiver"]; assert stat.S_IMODE(s.st_mode)==0o600 and stat.S_IMODE(d.st_mode)==0o700 and target["image"]==image and "build" not in target' "$recovery_snapshot" "$previous_id"
/usr/bin/python3 -c 'import json,sys; assert json.load(open(sys.argv[1]))["services"]["health-receiver"]["pull_policy"]=="never"' "$recovery_snapshot"
grep -Fq -- "-f $recovery_snapshot -p health-test up -d --no-deps health-receiver" "$preout" || { printf 'FAIL: recovery command does not reference private snapshot\n' >&2; exit 1; }
grep -Fq -- "rm -rf -- $(dirname "$recovery_snapshot")" "$preout" || { printf 'FAIL: secure recovery snapshot cleanup instruction missing\n' >&2; exit 1; }
[[ $(grep -c '<config> <--format> <json>' "$command_log") == 2 ]] || { printf 'FAIL: recovery snapshot was not Compose-validated\n' >&2; exit 1; }
if grep -q 'docker-run-stderr-canary-secret\|do-not-leak-this-secret' "$preout" "$command_log"; then printf 'FAIL: secret leaked\n' >&2; exit 1; fi
if grep -q '<up>\|<exec>\|<logs>' "$command_log"; then printf 'FAIL: pre-audit failure continued deployment\n' >&2; exit 1; fi

# A partial previous-container inspection must never produce an executable
# fallback using Config.Image (especially a mutable tag).
partial=$tmp/partial.out
reset_fixture
if PATH="$tmp/bin:$PATH" FAKE_COMMAND_LOG="$command_log" FAKE_REPO_DIGESTS="$digest_a" FAKE_TARGET_DIGEST="$digest_a" \
    FAKE_PREVIOUS_ID="$previous_id" FAKE_STATE_DIR="$state_dir" FAKE_FAIL_PREVIOUS_ID=1 FAKE_COMPOSE_FILE="$tmp/compose/compose.yml" \
    FAKE_ORIGINAL_ENV="$tmp/audit.env" FAKE_DEPENDENCY_FILE="$tmp/compose/dependency.env" \
    "$test_gate" --image "$mutable" --compose-dir "$tmp/compose" --compose-file compose.yml --project health-test \
      --service health-receiver --primary-schema health_primary --audit-env "$tmp/audit.env" --network health-net >"$partial" 2>&1; then
  printf 'FAIL: partial previous inspection succeeded\n' >&2; exit 1
fi
grep -q 'validated private snapshot.*unavailable' "$partial" || { printf 'FAIL: safe partial-inspect message missing\n' >&2; exit 1; }
if grep -q 'Reviewed recovery command\|Private recovery Compose snapshot:' "$partial"; then printf 'FAIL: unsafe partial-inspect recovery command printed\n' >&2; exit 1; fi
if grep -q 'inspect canary-secret' "$partial"; then printf 'FAIL: inspect stderr leaked\n' >&2; exit 1; fi

# A source Compose file that hardcodes the audited target image must not make
# the printed recovery command restart that failed target. The persistent
# effective snapshot is independently pinned to the prior immutable image ID.
hardcoded_out=$(expect_failure hardcoded-recovery preaudit '' '' "$digest_a" hardcoded-compose)
grep -Fq "image: $digest_a" "$tmp/compose/compose.yml" || { printf 'FAIL: hardcoded target fixture was not active\n' >&2; exit 1; }
hardcoded_snapshot=$(sed -n 's/^Private recovery Compose snapshot: //p' "$hardcoded_out")
[[ -f $hardcoded_snapshot ]] || { printf 'FAIL: hardcoded-image recovery snapshot missing\n' >&2; exit 1; }
/usr/bin/python3 -c 'import json,sys; target=json.load(open(sys.argv[1]))["services"]["health-receiver"]; assert target["image"]==sys.argv[2] and target["image"]!=sys.argv[3] and "build" not in target' "$hardcoded_snapshot" "$previous_id" "$digest_a"
grep -Fq -- "-f $hardcoded_snapshot -p health-test up -d --no-deps health-receiver" "$hardcoded_out" || { printf 'FAIL: hardcoded-image recovery command reused source Compose\n' >&2; exit 1; }

# An original pull_policy=always must be overridden so recovery cannot pull a
# different image for the old reference.
pull_out=$(expect_failure recovery-pull-policy preaudit '' '' "$digest_a" pull-policy-always)
pull_snapshot=$(sed -n 's/^Private recovery Compose snapshot: //p' "$pull_out")
/usr/bin/python3 -c 'import json,sys; target=json.load(open(sys.argv[1]))["services"]["health-receiver"]; assert target["pull_policy"]=="never" and target["image"]==sys.argv[2]' "$pull_snapshot" "$previous_id"

# The persistent artifact keeps only the selected service and the top-level
# resources it references. Unrelated service configuration and secrets must
# not survive merely because they appeared in the rendered source model.
rich_out=$(expect_failure recovery-minimal preaudit '' '' "$digest_a" recovery-rich)
rich_snapshot=$(sed -n 's/^Private recovery Compose snapshot: //p' "$rich_out")
/usr/bin/python3 -c '
import json,sys
v=json.load(open(sys.argv[1])); target=v["services"]["health-receiver"]
assert set(v["services"])=={"health-receiver"} and "depends_on" not in target
assert set(v["networks"])=={"app"} and set(v["volumes"])=={"health-data"} and set(v["secrets"])=={"health-secret"}
assert "must-not-persist" not in open(sys.argv[1]).read() and "unrelated-secret" not in v["secrets"]
' "$rich_snapshot"

# Static JSON checks are not sufficient: if Compose interprets the snapshot as
# a different image or reintroduces build, no executable recovery is printed.
for recovery_fault in recovery-drift recovery-build; do
  fault_out=$(expect_failure "$recovery_fault" preaudit '' '' "$digest_a" "$recovery_fault")
  grep -q 'validated private snapshot.*unavailable' "$fault_out" || { printf 'FAIL: %s did not suppress recovery\n' "$recovery_fault" >&2; exit 1; }
  if grep -q 'Reviewed recovery command\|Private recovery Compose snapshot:' "$fault_out"; then printf 'FAIL: %s printed unsafe recovery\n' "$recovery_fault" >&2; exit 1; fi
  [[ $(grep -c '<config> <--format> <json>' "$command_log") == 2 ]] || { printf 'FAIL: %s did not exercise Compose validation\n' "$recovery_fault" >&2; exit 1; }
done

expect_failure migration-contradiction '' '' migration-contradiction >/dev/null
expect_failure audit-contradiction '' '' audit-contradiction >/dev/null
expect_failure invalid-version '' '' invalid-version >/dev/null
expect_failure invalid-checksum '' '' invalid-checksum >/dev/null
expect_failure zero-probes '' '' zero-probes >/dev/null
expect_failure marker-mismatch '' '' marker-mismatch >/dev/null
expect_failure contract-mismatch-pre '' '' contract-mismatch-pre >/dev/null
expect_failure contract-mismatch-post '' '' contract-mismatch-post >/dev/null
for scenario in timeout restart restart-after image-mismatch fatal replacement; do expect_failure "runtime-$scenario" '' "$scenario" >/dev/null; done

for special in ignored-image build-override missing-network malformed-env; do
  special_out=$(expect_failure "prestop-$special" '' '' '' "$digest_a" "$special")
  if grep -q '<stop>' "$command_log"; then printf 'FAIL: %s reached service stop\n' "$special" >&2; exit 1; fi
  if [[ $special == malformed-env ]] && grep -q 'malformed-env-canary' "$special_out"; then printf 'FAIL: malformed env launch stderr leaked\n' >&2; exit 1; fi
done

# Digest selection: unrelated entries are ignored, identical matching entries
# deduplicate, but zero/nonmatching/conflicting matches fail.
run_gate "$tmp/multi-one.out" '' '' '' "$unrelated\n$digest_a"
run_gate "$tmp/duplicate.out" '' '' '' "$digest_a\n$digest_a"
expect_failure digest-zero '' '' '' '' >/dev/null
expect_failure digest-nonmatch '' '' '' "$unrelated" >/dev/null
expect_failure digest-conflict '' '' '' "$digest_a\n$digest_d" >/dev/null

# Exact requested digest must also match inspection rather than merely sharing
# a repository. This direct run uses the same trusted fixtures.
reset_fixture
if PATH="$tmp/bin:$PATH" FAKE_COMMAND_LOG="$command_log" FAKE_REPO_DIGESTS="$digest_d" FAKE_TARGET_DIGEST="$digest_d" \
    FAKE_PREVIOUS_ID="$previous_id" FAKE_STATE_DIR="$state_dir" FAKE_COMPOSE_FILE="$tmp/compose/compose.yml" \
    "$test_gate" --image "$digest_a" --compose-dir "$tmp/compose" --compose-file compose.yml --project health-test \
      --service health-receiver --primary-schema health_primary --audit-env "$tmp/audit.env" --network health-net >"$tmp/exact-mismatch.out" 2>&1; then
  printf 'FAIL: exact requested digest mismatch accepted\n' >&2; exit 1
fi

# O_NOFOLLOW rejects an audit-env symlink. A compose mutation after the first
# compose call is caught by the fingerprint before service stop.
ln -s "$tmp/audit.env" "$tmp/audit-link.env"
reset_fixture
if PATH="$tmp/bin:$PATH" FAKE_COMMAND_LOG="$command_log" FAKE_REPO_DIGESTS="$digest_a" FAKE_TARGET_DIGEST="$digest_a" \
    FAKE_PREVIOUS_ID="$previous_id" FAKE_STATE_DIR="$state_dir" FAKE_COMPOSE_FILE="$tmp/compose/compose.yml" \
    "$test_gate" --image "$mutable" --compose-dir "$tmp/compose" --compose-file compose.yml --project health-test \
      --service health-receiver --primary-schema health_primary --audit-env "$tmp/audit-link.env" --network health-net >"$tmp/symlink.out" 2>&1; then
  printf 'FAIL: symlink audit env accepted\n' >&2; exit 1
fi
swap_out=$(expect_failure swap-during-render '' '' '' "$digest_a" render-swap)
grep -q 'identity or content changed' "$swap_out" || { printf 'FAIL: compose swap reason missing\n' >&2; exit 1; }

ln -s "$tmp/compose/compose.yml" "$tmp/compose/compose-link.yml"
reset_fixture
if PATH="$tmp/bin:$PATH" FAKE_COMMAND_LOG="$command_log" "$test_gate" --image "$mutable" \
    --compose-dir "$tmp/compose" --compose-file compose-link.yml --project health-test --service health-receiver \
    --primary-schema health_primary --audit-env "$tmp/audit.env" --network health-net >"$tmp/compose-symlink.out" 2>&1; then
  printf 'FAIL: symlink compose file accepted\n' >&2; exit 1
fi
[[ ! -s $command_log ]] || { printf 'FAIL: symlink compose file invoked Docker\n' >&2; exit 1; }
chmod 777 "$tmp/compose"
reset_fixture
if PATH="$tmp/bin:$PATH" FAKE_COMMAND_LOG="$command_log" "$test_gate" --image "$mutable" \
    --compose-dir "$tmp/compose" --compose-file compose.yml --project health-test --service health-receiver \
    --primary-schema health_primary --audit-env "$tmp/audit.env" --network health-net >"$tmp/untrusted-compose.out" 2>&1; then
  printf 'FAIL: group/world-writable compose directory accepted\n' >&2; exit 1
fi
[[ ! -s $command_log ]] || { printf 'FAIL: untrusted compose directory invoked Docker\n' >&2; exit 1; }
chmod 755 "$tmp/compose"

# Early option validation must happen before Docker, and an owner-executable
# audit file is not a subset of 0600.
reset_fixture
if PATH="$tmp/bin:$PATH" FAKE_COMMAND_LOG="$command_log" "$test_gate" --image --help \
    --compose-dir "$tmp/compose" --compose-file compose.yml --project health-test --service health-receiver \
    --primary-schema health_primary --audit-env "$tmp/audit.env" --network health-net >"$tmp/leading-dash.out" 2>&1; then
  printf 'FAIL: leading-dash image accepted\n' >&2; exit 1
fi
[[ ! -s $command_log ]] || { printf 'FAIL: leading-dash image invoked Docker\n' >&2; exit 1; }
reset_fixture
chmod 700 "$tmp/audit.env"
if PATH="$tmp/bin:$PATH" FAKE_COMMAND_LOG="$command_log" "$test_gate" --image "$mutable" \
    --compose-dir "$tmp/compose" --compose-file compose.yml --project health-test --service health-receiver \
    --primary-schema health_primary --audit-env "$tmp/audit.env" --network health-net >"$tmp/mode-0700.out" 2>&1; then
  printf 'FAIL: audit env mode 0700 accepted\n' >&2; exit 1
fi
[[ ! -s $command_log ]] || { printf 'FAIL: invalid audit mode invoked Docker\n' >&2; exit 1; }
chmod 600 "$tmp/audit.env"

if find "$tmp/gate-tmp" -maxdepth 1 -type d -name 'health-schema-gate.*' | grep -q .; then
  printf 'FAIL: release gate temp directories were not cleaned\n' >&2; exit 1
fi
printf 'PASS: hardened tenant schema release gate fake tests\n'
