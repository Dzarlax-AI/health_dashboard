#!/bin/bash
set -euo pipefail

required_euid=0
trusted_uid=0
allow_untrusted_ancestors_for_tests=0
if ((EUID != required_euid)); then
  printf 'configuration error: release gate must run as root\n' >&2
  exit 2
fi

# Never inherit a root caller's PATH. Every external command used below is
# resolved from this fixed list and its resolved path is verified before use.
trusted_path='/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin'
PATH=$trusted_path
export PATH
PYTHON=/usr/bin/python3
[[ -x $PYTHON ]] || { printf 'configuration error: trusted python3 is unavailable\n' >&2; exit 2; }

usage() {
  printf '%s\n' \
    'Usage: deploy-tenant-schema-gate.sh [options]' '' \
    'Required:' \
    '  --image REF              Requested image tag or digest to pull and pin' \
    '  --compose-dir DIR        Compose project directory' \
    '  --compose-file FILE      Compose file (absolute or relative to compose dir)' \
    '  --project NAME           Compose project name' \
    '  --service NAME           Compose service to stop and start' \
    '  --primary-schema NAME    Canonical primary tenant schema' \
    '  --audit-env FILE         Root-owned, mode subset of 0600 CLI environment' \
    '  --network NAME           Docker network used by one-shot CLI containers' '' \
    'Optional:' \
    '  --readiness-timeout SEC  Stability deadline in seconds (default: 120)' \
    '  --help                   Show this help'
}

requested_image=${HEALTH_GATE_IMAGE:-}
compose_dir=${HEALTH_GATE_COMPOSE_DIR:-}
compose_file=${HEALTH_GATE_COMPOSE_FILE:-}
compose_project=${HEALTH_GATE_PROJECT:-}
service=${HEALTH_GATE_SERVICE:-}
primary_schema=${HEALTH_GATE_PRIMARY_SCHEMA:-}
audit_env=${HEALTH_GATE_AUDIT_ENV:-}
network=${HEALTH_GATE_NETWORK:-}
readiness_timeout=${HEALTH_GATE_READINESS_TIMEOUT:-120}

while (($# > 0)); do
  case "$1" in
    --image|--compose-dir|--compose-file|--project|--service|--primary-schema|--audit-env|--network|--readiness-timeout)
      (($# >= 2)) || { printf 'missing value for %s\n' "$1" >&2; exit 2; }
      case "$1" in
        --image) requested_image=$2 ;;
        --compose-dir) compose_dir=$2 ;;
        --compose-file) compose_file=$2 ;;
        --project) compose_project=$2 ;;
        --service) service=$2 ;;
        --primary-schema) primary_schema=$2 ;;
        --audit-env) audit_env=$2 ;;
        --network) network=$2 ;;
        --readiness-timeout) readiness_timeout=$2 ;;
      esac
      shift 2
      ;;
    --help|-h) usage; exit 0 ;;
    *) printf 'unknown option: %s\n' "$1" >&2; usage >&2; exit 2 ;;
  esac
done

fail_usage() { printf 'configuration error: %s\n' "$1" >&2; exit 2; }
require_value() {
  local name=$1 value=$2
  [[ -n $value ]] || fail_usage "$name is required"
  [[ $value != *$'\n'* && $value != *$'\r'* ]] || fail_usage "$name contains a newline"
}
validate_identifier() {
  local name=$1 value=$2
  require_value "$name" "$value"
  [[ $value =~ ^[A-Za-z0-9][A-Za-z0-9_.-]*$ ]] || fail_usage "$name is not a safe Docker identifier"
}

require_value image "$requested_image"
[[ $requested_image != -* ]] || fail_usage 'image must not start with an option prefix'
[[ $requested_image != *[[:space:]]* ]] || fail_usage 'image contains whitespace'
[[ $requested_image =~ ^[A-Za-z0-9][A-Za-z0-9._/:@-]*$ ]] || fail_usage 'image contains characters outside the supported Docker reference grammar'
require_value compose-dir "$compose_dir"
require_value compose-file "$compose_file"
validate_identifier project "$compose_project"
validate_identifier service "$service"
validate_identifier network "$network"
require_value primary-schema "$primary_schema"
[[ $primary_schema =~ ^[a-z][a-z0-9_]{0,62}$ ]] || fail_usage 'primary-schema is not a canonical PostgreSQL identifier'
require_value audit-env "$audit_env"
[[ $readiness_timeout =~ ^[1-9][0-9]*$ ]] || fail_usage 'readiness-timeout must be a positive integer'

validate_trusted_executable() {
  local path=$1
  "$PYTHON" -c '
import os, stat, sys
path=sys.argv[1]; uid=int(sys.argv[2]); allow=int(sys.argv[3]); owners={uid} if uid == 0 else {0,uid}
if not os.path.isabs(path): raise SystemExit(1)
real = os.path.realpath(path)
st = os.stat(real)
if not stat.S_ISREG(st.st_mode) or st.st_uid not in owners or st.st_mode & 0o022: raise SystemExit(1)
cur = os.path.dirname(real)
while True:
    s = os.stat(cur)
    if not stat.S_ISDIR(s.st_mode) or s.st_uid not in owners or (not allow and s.st_mode & 0o022): raise SystemExit(1)
    parent = os.path.dirname(cur)
    if parent == cur: break
    cur = parent
' "$path" "$trusted_uid" "$allow_untrusted_ancestors_for_tests"
}

# The fixed interpreter bootstraps validation. Its own real path and every
# subsequently resolved tool must be root-owned and not group/world-writable.
validate_trusted_executable "$PYTHON" || fail_usage 'python3 path is not trusted'
for path_dir in ${PATH//:/ }; do
  "$PYTHON" -c '
import os, stat, sys
p=os.path.realpath(sys.argv[1]); uid=int(sys.argv[2]); s=os.stat(p); owners={uid} if uid == 0 else {0,uid}
raise SystemExit(0 if stat.S_ISDIR(s.st_mode) and s.st_uid in owners and not (s.st_mode & 0o022) else 1)
' "$path_dir" "$trusted_uid" || fail_usage 'PATH contains an untrusted directory'
done

resolve_tool() {
  local name=$1 resolved
  resolved=$(command -v "$name") || fail_usage "required command is unavailable: $name"
  [[ $resolved = /* ]] || fail_usage "required command did not resolve to an absolute path: $name"
  validate_trusted_executable "$resolved" || fail_usage "required command path is not trusted: $name"
  printf '%s\n' "$resolved"
}
DOCKER=$(resolve_tool docker)
MKTEMP=$(resolve_tool mktemp)
CHMOD=$(resolve_tool chmod)
RM=$(resolve_tool rm)
GREP=$(resolve_tool grep)
SLEEP=$(resolve_tool sleep)

umask 077
temp_base=/var/tmp
tmp_dir=$($MKTEMP -d "$temp_base/health-schema-gate.XXXXXX")
$CHMOD 700 "$tmp_dir"
stage=trusted-input-validation
success=0
target_digest=''
previous_container=''
previous_image=''
previous_image_id=''
previous_restart_count=''
compose_fingerprint=''
compose_path=''
compose_dir_canonical=''
audit_env_snapshot=$tmp_dir/audit.env

print_recovery() {
  printf 'Release gate failed during stage: %s\n' "$stage" >&2
  if [[ $previous_container =~ ^[A-Za-z0-9_.-]+$ ]]; then printf 'Previous container ID: %s\n' "$previous_container" >&2; fi
  if [[ $previous_image =~ ^[A-Za-z0-9][A-Za-z0-9._/:@-]*$ ]]; then printf 'Previous image reference: %s\n' "$previous_image" >&2; fi
  if [[ $previous_image_id =~ ^sha256:[0-9a-f]{64}$ ]]; then
    printf 'Previous immutable local image ID: %s\n' "$previous_image_id" >&2
    printf 'Reviewed recovery template (verify database backward compatibility first):\n  HEALTH_IMAGE=' >&2
    printf '%q' "$previous_image_id" >&2
    printf ' docker compose --project-directory ' >&2; printf '%q' "$compose_dir_canonical" >&2
    printf ' -f ' >&2; printf '%q' "$compose_path" >&2
    printf ' -p ' >&2; printf '%q' "$compose_project" >&2
    printf ' up -d --no-deps ' >&2; printf '%q\n' "$service" >&2
  else
    printf 'Previous immutable image ID is unavailable; no executable recovery command can be printed safely.\n' >&2
  fi
  printf 'No database rollback or old-image restart was attempted automatically.\n' >&2
}
on_exit() {
  local rc=$?
  $RM -rf "$tmp_dir"
  if ((rc != 0)) && ((success == 0)); then print_recovery; fi
}
trap on_exit EXIT

# Open the operator file once with O_NOFOLLOW, validate the open descriptor and
# trusted parent chain, then copy it to a private snapshot. Docker never sees the
# mutable operator path.
if ! "$PYTHON" -c '
import os, stat, sys
src,dst=map(os.path.abspath,sys.argv[1:3]); uid=int(sys.argv[3]); allow=int(sys.argv[4]); owners={uid} if uid == 0 else {0,uid}
parent = os.path.dirname(src)
while True:
    if os.path.realpath(parent) != parent: raise SystemExit(1)
    s=os.stat(parent)
    if not stat.S_ISDIR(s.st_mode) or s.st_uid not in owners or (not allow and s.st_mode & 0o022): raise SystemExit(1)
    nxt=os.path.dirname(parent)
    if nxt == parent: break
    parent=nxt
nofollow=getattr(os, "O_NOFOLLOW", None)
if nofollow is None: raise SystemExit(1)
fd=os.open(src, os.O_RDONLY | nofollow)
try:
    s=os.fstat(fd)
    if not stat.S_ISREG(s.st_mode) or s.st_uid != uid or (stat.S_IMODE(s.st_mode) | 0o600) != 0o600: raise SystemExit(1)
    out=os.open(dst, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        while True:
            chunk=os.read(fd, 65536)
            if not chunk: break
            view=memoryview(chunk)
            while view: view=view[os.write(out, view):]
        os.fsync(out)
    finally: os.close(out)
finally: os.close(fd)
' "$audit_env" "$audit_env_snapshot" "$trusted_uid" "$allow_untrusted_ancestors_for_tests"; then
  fail_usage 'audit-env or its parent chain is not trusted'
fi
$CHMOD 600 "$audit_env_snapshot"

if [[ $compose_file = /* ]]; then compose_candidate=$compose_file; else compose_candidate=$compose_dir/$compose_file; fi
fingerprint_compose() {
  "$PYTHON" -c '
import hashlib, os, stat, sys
d=os.path.abspath(sys.argv[1]); p=os.path.abspath(sys.argv[2]); uid=int(sys.argv[3]); allow=int(sys.argv[4]); owners={uid} if uid == 0 else {0,uid}
if os.path.realpath(d) != d or os.path.realpath(p) != p: raise SystemExit(1)
ds=os.stat(d)
if not stat.S_ISDIR(ds.st_mode) or ds.st_uid != uid or ds.st_mode & 0o022: raise SystemExit(1)
for cur in (d, os.path.dirname(p)):
    while True:
        s=os.stat(cur)
        if not stat.S_ISDIR(s.st_mode) or s.st_uid not in owners or (not allow and s.st_mode & 0o022): raise SystemExit(1)
        nxt=os.path.dirname(cur)
        if nxt == cur: break
        cur=nxt
nofollow=getattr(os, "O_NOFOLLOW", None)
if nofollow is None: raise SystemExit(1)
fd=os.open(p, os.O_RDONLY | nofollow)
try:
    s=os.fstat(fd)
    if not stat.S_ISREG(s.st_mode) or s.st_uid != uid or s.st_mode & 0o022: raise SystemExit(1)
    h=hashlib.sha256()
    while True:
        b=os.read(fd, 65536)
        if not b: break
        h.update(b)
finally: os.close(fd)
print(f"{d}\t{p}\t{s.st_dev}\t{s.st_ino}\t{h.hexdigest()}")
' "$compose_dir" "$compose_candidate" "$trusted_uid" "$allow_untrusted_ancestors_for_tests"
}
if ! compose_fingerprint=$(fingerprint_compose); then fail_usage 'compose file, directory, or parent chain is not trusted'; fi
IFS=$'\t' read -r compose_dir_canonical compose_path _dev _ino _sum <<<"$compose_fingerprint"

verify_compose_unchanged() {
  local current
  current=$(fingerprint_compose) || { printf 'compose trust validation failed\n' >&2; return 1; }
  [[ $current == "$compose_fingerprint" ]] || { printf 'compose file identity or content changed during deployment\n' >&2; return 1; }
}
"$DOCKER" compose version >/dev/null 2>&1 || fail_usage 'modern docker compose is unavailable'

json_summary_and_status() {
  local json_file=$1 kind=$2
  "$PYTHON" -c '
import json, sys
with open(sys.argv[1], "r", encoding="utf-8") as f: v=json.load(f)
kind=sys.argv[2]; status=v.get("status")
if status not in ("pass", "fail"): raise SystemExit(1)
if v.get("error") not in (None,""): raise SystemExit(1)
version=v.get("target_contract_version"); checksum=v.get("target_contract_checksum")
if not isinstance(version,int) or isinstance(version,bool) or version <= 0: raise SystemExit(1)
if not isinstance(checksum,str) or not __import__("re").fullmatch(r"[0-9a-f]{64}",checksum): raise SystemExit(1)
if kind == "migration":
    a=v.get("attempted"); c=v.get("completed")
    if not isinstance(a,int) or isinstance(a,bool) or not isinstance(c,int) or isinstance(c,bool): raise SystemExit(1)
    if status == "pass" and not (a > 0 and a == c): raise SystemExit(1)
    print(f"{status}\t{version}\t{checksum}\tmigration status={status} attempted={a} completed={c}")
else:
    findings=v.get("findings"); counts=v.get("counts"); probes=v.get("probes")
    if not isinstance(findings,list) or not isinstance(counts,dict) or not isinstance(probes,dict): raise SystemExit(1)
    reg=counts.get("registry_by_state"); markers=counts.get("markers"); roles=counts.get("roles")
    attempted=probes.get("attempted"); denied=probes.get("denied"); failed=probes.get("failed")
    active=reg.get("active",0) if isinstance(reg,dict) else None
    vals=(active,markers,roles,attempted,denied,failed)
    if not all(isinstance(x,int) and not isinstance(x,bool) for x in vals): raise SystemExit(1)
    if status == "pass" and not (active > 0 and markers == active and roles == active and attempted > 0 and not findings and failed == 0 and denied == attempted): raise SystemExit(1)
    print(f"{status}\t{version}\t{checksum}\taudit status={status} active={active} findings={len(findings)} markers={markers} roles={roles} probes={attempted} probe_failures={failed}")
' "$json_file" "$kind"
}
run_tenant_cli() {
  local label=$1 kind=$2 policy=$3 output_file=$4 error_file=$5
  shift 5
  local cli_rc=0 parsed status summary
  stage=$label
  "$DOCKER" run --rm --network "$network" --env-file "$audit_env_snapshot" \
    --entrypoint /app/tenant_isolation "$target_digest" "$@" >"$output_file" 2>"$error_file" || cli_rc=$?
  if ! parsed=$(json_summary_and_status "$output_file" "$kind" 2>/dev/null); then
    printf '%s produced no valid sanitized JSON summary\n' "$label" >&2; return 1
  fi
  IFS=$'\t' read -r status last_contract_version last_contract_checksum summary <<<"$parsed"
  printf '%s\n' "$summary"
  if [[ $policy == transport ]]; then
    if [[ $status == pass && $cli_rc == 0 ]] || [[ $status == fail && $cli_rc == 1 ]]; then return 0; fi
    printf '%s did not produce a valid logical audit outcome\n' "$label" >&2; return 1
  fi
  ((cli_rc == 0)) || { printf '%s container exited nonzero; details suppressed\n' "$label" >&2; return 1; }
  [[ $status == pass ]] || { printf '%s did not pass\n' "$label" >&2; return 1; }
}

stage=pull
printf 'Pulling requested image reference...\n'
"$DOCKER" pull "$requested_image" >/dev/null
stage=resolve-image-digest
digest_file=$tmp_dir/repository-digests.txt
"$DOCKER" image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$requested_image" >"$digest_file"
if ! target_digest=$("$PYTHON" -c '
import re, sys
requested=sys.argv[1]
lines=[x.strip() for x in open(sys.argv[2],encoding="utf-8") if x.strip()]
def repo(ref):
    base=ref.split("@",1)[0]
    slash=base.rfind("/"); colon=base.rfind(":")
    if colon > slash: base=base[:colon]
    first=base.split("/",1)[0]
    if "." not in first and ":" not in first and first != "localhost":
        base="docker.io/" + (base if "/" in base else "library/"+base)
    return base.lower()
want_repo=repo(requested)
want_digest=requested.split("@",1)[1] if "@" in requested else None
pat=re.compile(r"^([^\s@]+)@(sha256:[0-9a-f]{64})$")
matches={}
for line in lines:
    m=pat.match(line)
    if m and repo(m.group(1)) == want_repo: matches.setdefault(m.group(2),set()).add(f"{m.group(1)}@{m.group(2)}")
if len(matches) != 1: raise SystemExit(1)
digest, values=next(iter(matches.items())); value=sorted(values)[0]
if want_digest is not None and digest != want_digest: raise SystemExit(1)
print(value)
' "$requested_image" "$digest_file"); then
  printf 'requested image did not resolve to exactly one matching immutable repository digest\n' >&2; exit 1
fi
[[ $target_digest =~ ^[A-Za-z0-9][A-Za-z0-9._/:-]*@sha256:[0-9a-f]{64}$ ]] || { printf 'resolved repository digest has an unsafe format\n' >&2; exit 1; }
resolved_image_id_file=$tmp_dir/resolved-image-id.txt
"$DOCKER" image inspect --format '{{.Id}}' "$target_digest" >"$resolved_image_id_file"
"$PYTHON" -c '
import re,sys
value=open(sys.argv[1],encoding="utf-8").read().strip()
raise SystemExit(0 if re.fullmatch(r"sha256:[0-9a-f]{64}",value) else 1)
' "$resolved_image_id_file" || { printf 'resolved repository digest could not be verified locally\n' >&2; exit 1; }
printf 'Resolved immutable target digest: %s\n' "$target_digest"

stage=render-effective-compose
rendered_compose=$tmp_dir/effective-compose.json
rendered_compose_err=$tmp_dir/effective-compose.stderr
: >"$rendered_compose"; : >"$rendered_compose_err"; $CHMOD 600 "$rendered_compose" "$rendered_compose_err"
verify_compose_unchanged
HEALTH_IMAGE=$target_digest "$DOCKER" compose --project-directory "$compose_dir_canonical" -f "$compose_path" -p "$compose_project" \
  config --format json >"$rendered_compose" 2>"$rendered_compose_err" || { printf 'effective compose rendering failed; details suppressed\n' >&2; exit 1; }
verify_compose_unchanged
rendered_fingerprint=$("$PYTHON" -c '
import hashlib,json,os,re,stat,sys
p,service,image=sys.argv[1:4]; uid=int(sys.argv[4])
fd=os.open(p,os.O_RDONLY|getattr(os,"O_NOFOLLOW"))
try:
 s=os.fstat(fd); raw=b""
 while True:
  b=os.read(fd,65536)
  if not b: break
  raw+=b
finally: os.close(fd)
if not stat.S_ISREG(s.st_mode) or s.st_uid != uid or stat.S_IMODE(s.st_mode) != 0o600: raise SystemExit(1)
v=json.loads(raw); services=v.get("services")
if not isinstance(services,dict) or service not in services or not isinstance(services[service],dict): raise SystemExit(1)
target=services[service]
if target.get("image") != image or ("build" in target and target.get("build") is not None): raise SystemExit(1)
if "include" in v or "extends" in target: raise SystemExit(1)
print(f"{s.st_dev}\t{s.st_ino}\t{hashlib.sha256(raw).hexdigest()}")
' "$rendered_compose" "$service" "$target_digest" "$trusted_uid") || { printf 'effective compose does not pin the selected service digest or contains a build override\n' >&2; exit 1; }
verify_rendered_compose() {
  local current
  current=$("$PYTHON" -c '
import hashlib,os,stat,sys
p=sys.argv[1]; uid=int(sys.argv[2]); fd=os.open(p,os.O_RDONLY|getattr(os,"O_NOFOLLOW"))
try:
 s=os.fstat(fd); h=hashlib.sha256()
 while True:
  b=os.read(fd,65536)
  if not b: break
  h.update(b)
finally: os.close(fd)
if not stat.S_ISREG(s.st_mode) or s.st_uid != uid or stat.S_IMODE(s.st_mode) != 0o600: raise SystemExit(1)
print(f"{s.st_dev}\t{s.st_ino}\t{h.hexdigest()}")
' "$rendered_compose" "$trusted_uid") || return 1
  [[ $current == "$rendered_fingerprint" ]]
}
compose() {
  verify_rendered_compose || { printf 'secured effective compose snapshot changed\n' >&2; return 1; }
  "$DOCKER" compose --project-directory "$compose_dir_canonical" -f "$rendered_compose" -p "$compose_project" "$@"
}

transport_json=$tmp_dir/transport-audit.json; transport_err=$tmp_dir/transport-audit.stderr
migration_json=$tmp_dir/migration.json; migration_err=$tmp_dir/migration.stderr
preaudit_json=$tmp_dir/preaudit.json; preaudit_err=$tmp_dir/preaudit.stderr
postaudit_json=$tmp_dir/postaudit.json; postaudit_err=$tmp_dir/postaudit.stderr
logs_file=$tmp_dir/recent.log; network_file=$tmp_dir/network.json; network_err=$tmp_dir/network.stderr
for f in "$transport_json" "$transport_err" "$migration_json" "$migration_err" "$preaudit_json" "$preaudit_err" "$postaudit_json" "$postaudit_err" "$logs_file" "$network_file" "$network_err"; do : >"$f"; $CHMOD 600 "$f"; done

stage=network-preflight
"$DOCKER" network inspect "$network" >"$network_file" 2>"$network_err" || { printf 'configured one-shot Docker network is unavailable\n' >&2; exit 1; }
run_tenant_cli transport-audit-preflight audit transport "$transport_json" "$transport_err" --mode audit --all --primary-schema "$primary_schema"
transport_contract_version=$last_contract_version
transport_contract_checksum=$last_contract_checksum

stage=inspect-previous-service
previous_container=$(compose ps -q "$service")
if [[ -n $previous_container ]]; then
  [[ $previous_container =~ ^[A-Za-z0-9_.-]+$ ]] || { printf 'compose returned malformed previous container identifiers\n' >&2; exit 1; }
  previous_image=$("$DOCKER" inspect --format '{{.Config.Image}}' "$previous_container" 2>/dev/null) || { previous_image=''; exit 1; }
  previous_restart_count=$("$DOCKER" inspect --format '{{.RestartCount}}' "$previous_container" 2>/dev/null) || { previous_restart_count=''; exit 1; }
  if ! previous_image_id=$("$DOCKER" inspect --format '{{.Image}}' "$previous_container" 2>/dev/null); then previous_image_id=''; exit 1; fi
  [[ $previous_image != *[[:space:]]* && $previous_restart_count =~ ^[0-9]+$ && $previous_image_id =~ ^sha256:[0-9a-f]{64}$ ]] || { printf 'previous container metadata is malformed\n' >&2; exit 1; }
  printf 'Recorded previous container and image metadata.\n'
fi

stage=stop-service
compose stop "$service"
run_tenant_cli migrate-contract migration required "$migration_json" "$migration_err" --mode migrate-contract --all --primary-schema "$primary_schema" --confirm
migration_contract_version=$last_contract_version; migration_contract_checksum=$last_contract_checksum
[[ $transport_contract_version == "$migration_contract_version" && $transport_contract_checksum == "$migration_contract_checksum" ]] || { printf 'target contract identity changed between transport preflight and migration\n' >&2; exit 1; }
run_tenant_cli pre-start-audit audit required "$preaudit_json" "$preaudit_err" --mode audit --all --primary-schema "$primary_schema"
[[ $last_contract_version == "$migration_contract_version" && $last_contract_checksum == "$migration_contract_checksum" ]] || { printf 'pre-start audit contract identity differs from migration\n' >&2; exit 1; }

stage=start-target
deployment_started_at=$("$PYTHON" -c 'import datetime; print(datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00","Z"))')
HEALTH_IMAGE=$target_digest compose up -d --no-deps "$service"
target_container=$(compose ps -q "$service")
[[ $target_container =~ ^[A-Za-z0-9_.-]+$ ]] || { printf 'compose did not return exactly one target container\n' >&2; exit 1; }
if [[ $target_container == "$previous_container" ]]; then expected_restart=$previous_restart_count; else expected_restart=0; fi

wait_for_stability() {
  local phase=$1 consecutive=0 current image running restarts
  local deadline=$((SECONDS + readiness_timeout))
  stage=$phase
  while ((SECONDS < deadline)); do
    current=$(compose ps -q "$service")
    [[ $current == "$target_container" ]] || { printf 'target container was replaced during stability check\n' >&2; return 1; }
    image=$("$DOCKER" inspect --format '{{.Config.Image}}' "$current" 2>/dev/null)
    [[ $image == "$target_digest" ]] || { printf 'running service does not use the audited immutable digest\n' >&2; return 1; }
    running=$("$DOCKER" inspect --format '{{.State.Running}}' "$current" 2>/dev/null)
    restarts=$("$DOCKER" inspect --format '{{.RestartCount}}' "$current" 2>/dev/null)
    [[ $restarts == "$expected_restart" ]] || { printf 'target container restart count changed\n' >&2; return 1; }
    if [[ $running == true ]] && "$DOCKER" exec "$current" /bin/sh -c 'wget -qO- http://127.0.0.1:8080/readyz >/dev/null' >/dev/null 2>&1; then
      consecutive=$((consecutive + 1))
      if ((consecutive >= 3)); then printf '%s passed.\n' "$phase"; return 0; fi
    else
      consecutive=0
    fi
    $SLEEP "$stability_interval"
  done
  printf '%s deadline exceeded\n' "$phase" >&2
  return 1
}
stability_interval=1
wait_for_stability pre-audit-runtime-stability
run_tenant_cli post-start-audit audit required "$postaudit_json" "$postaudit_err" --mode audit --all --primary-schema "$primary_schema"
[[ $last_contract_version == "$migration_contract_version" && $last_contract_checksum == "$migration_contract_checksum" ]] || { printf 'post-start audit contract identity differs from migration\n' >&2; exit 1; }
wait_for_stability post-audit-runtime-stability

stage=runtime-logs
"$DOCKER" logs --since "$deployment_started_at" "$target_container" >"$logs_file" 2>&1 || { printf 'could not inspect deployment logs\n' >&2; exit 1; }
if "$GREP" -Eiq 'panic:|level[=:]fatal|(^|[[:space:]])fatal([:[:space:]]|$)|fatal error' "$logs_file"; then
  printf 'deployment logs contain a fatal marker; log content suppressed\n' >&2; exit 1
fi
stage=complete
success=1
printf 'Tenant schema release gate passed for %s.\n' "$target_digest"
