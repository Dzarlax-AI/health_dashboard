#!/bin/bash
set -euo pipefail
umask 077
case "${1:-}" in
  "") export SEMANTIC_READER_MODE=additive; artifact=semantic-performance.json ;;
  --replace) export SEMANTIC_READER_MODE=replacement; artifact=semantic-replacement-performance.json ;;
  --mcp-narrow) export SEMANTIC_READER_MODE=mcp-narrow; artifact=semantic-mcp-narrow-performance.json ;;
  --mcp-cache) export SEMANTIC_READER_MODE=mcp-cache; artifact=semantic-mcp-cache-performance.json ;;
  --night-card) export SEMANTIC_READER_MODE=night-card; artifact=semantic-night-card-performance.json ;;
  *) echo "Usage: $0 [--replace|--mcp-narrow|--mcp-cache|--night-card]" >&2; exit 2 ;;
esac
lab=$(mktemp -d /private/tmp/sreader.XXXXXXXX)
pgbin=${SEMANTIC_PG_BIN:-$(pg_config --bindir)}
root=$(cd "$(dirname "$0")/../../.." && pwd)
cd "$root"
pilot="$root/experiments/dbt_metrics"
export SEMANTIC_PERF_SRC="$pilot/semantic/performance" SEMANTIC_PERF_OUT="$lab"
client_test=

export GOCACHE="${GOCACHE:-$lab/go-cache}" GOPROXY=off
trap '"$pgbin/pg_ctl" -D "$lab/db" -m fast -w stop >/dev/null 2>&1 || true; rm -rf "$lab"; test -z "$client_test" || rm -f "$client_test"' EXIT
port=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
openssl rand -hex 20 > "$lab/password"
"$pgbin/initdb" -D "$lab/db" -U postgres --pwfile="$lab/password" --auth-host=scram-sha-256 --auth-local=trust --no-locale -E UTF8 >/dev/null
"$pgbin/pg_ctl" -D "$lab/db" -l "$lab/pg.log" -o "-k $lab -p $port -h 127.0.0.1 -c shared_preload_libraries=pg_stat_statements" -w start
export PGHOST="$lab" PGPORT="$port" PGUSER=postgres
"$pgbin/createdb" health_test
export PGDATABASE=health_test
"$pgbin/psql" -X -q -v ON_ERROR_STOP=1 -c "create table public.health_test_metadata(key text primary key,value text); insert into public.health_test_metadata values ('disposable_database','true')"
export HEALTH_ADMIN_DB_PASSWORD=$(openssl rand -hex 24)
export HEALTH_REGISTRY_DB_PASSWORD=$(openssl rand -hex 24)
base="127.0.0.1:$port/health_test?sslmode=disable"
export TENANT_DB_BOOTSTRAP_DATABASE_URL="postgres://postgres:$(cat "$lab/password")@$base" HEALTH_DB_TESTS=1
go run ./cmd/tenant_isolation --mode bootstrap-db-identities --manifest "$lab/identity.json" --confirm > "$lab/bootstrap.log"
"$pgbin/psql" -X -q -v ON_ERROR_STOP=1 -c 'grant select on public.health_test_metadata to health_admin,health_registry'
export READINESS_TEST_DSN="$TENANT_DB_BOOTSTRAP_DATABASE_URL"
export ADMIN_TEST_DSN="postgres://health_admin:$HEALTH_ADMIN_DB_PASSWORD@$base"
export REGISTRY_TEST_DSN="postgres://health_registry:$HEALTH_REGISTRY_DB_PASSWORD@$base"

export SEMANTIC_PG_PID=$(head -1 "$lab/db/postmaster.pid")
"$pgbin/psql" -X -q -v ON_ERROR_STOP=1 -c 'create extension pg_stat_statements; create extension pg_buffercache; create role bench_dbt login; create schema dbt_output authorization bench_dbt'
"$pgbin/psql" -X -q -v ON_ERROR_STOP=1 -f "$pilot/semantic/inputs.sql"
"$pgbin/psql" -X -q -v ON_ERROR_STOP=1 -c 'grant usage on schema source_data to bench_dbt; grant select on all tables in schema source_data to bench_dbt'
cat > "$lab/profiles.yml" <<PROFILE
health_dbt_metrics_pilot:
  target: local
  outputs:
    local:
      type: postgres
      host: $lab
      port: $port
      user: bench_dbt
      password: synthetic-only
      dbname: health_test
      schema: dbt_output
      threads: 1
PROFILE
models=(+semantic_sleep_hrv_day_v1 +semantic_sleep_duration_comparison_v1)
if [[ "$SEMANTIC_READER_MODE" == night-card ]]; then models+=(+semantic_sleep_night_card_v1); fi
DBT_SEND_ANONYMOUS_USAGE_STATS=false "$pilot/.venv/bin/dbt" run --project-dir "$pilot" --profiles-dir "$lab" --target-path "$lab/target" --log-path "$lab/logs" --vars '{semantic_shadow: true, sleep_duration_reference: true}' --select "${models[@]}"
python3 - "$root" "$lab" "$SEMANTIC_PERF_SRC" <<'PYOVERLAY'
import json,sys,os
from pathlib import Path
root,lab,source=map(Path,sys.argv[1:])
fixture=root/'internal/mcpserver/access_integration_test.go'
hook='if _, err := root.Exec(ctx, `UPDATE health_registry.users SET provisioning_state='
text=fixture.read_text()
assert text.count(hook)==1, 'disposable upstream fixture changed'
patched=lab/'fixture_test.go'
if os.getenv('SEMANTIC_READER_MODE') == 'night-card':
    text=text.replace('registerSleepComparisonTool(mcpServer)', 'registerSleepComparisonTool(mcpServer)\n\tregisterSemanticNightCard(mcpServer)')
    # Install exactly the view dependencies compiled by dbt, in topological order.
    manifest=json.loads((lab/'target/manifest.json').read_text())['nodes']
    models=[]
    def dependencies(key):
        node=manifest[key]
        for dep in node['depends_on']['nodes']:
            if dep.startswith('model.') and dep not in models:
                dependencies(dep)
        if key not in models:
            models.append(key)
    dependencies('model.health_dbt_metrics_pilot.semantic_sleep_night_card_v1')
    (lab/'card-views.json').write_text(json.dumps([manifest[k]['alias'] for k in models]))
patched.write_text(text.replace(hook,'runSemanticExperiment(t, root, users, manager, deriver, base.String(), httpRoutes, call, comparisonBody)\n\t'+hook))
overlay={str(fixture):str(patched), str(root/'internal/mcpserver/semantic_perf_test.go'):str(source/'readers_test.go.txt'),
         str(root/'internal/mcpserver/semantic_cache_test.go'):str(source/'cache_test.go.txt'),
         str(root/'internal/notify/semantic_perf.go'):str(source/'render.go.txt')}
if os.getenv('SEMANTIC_READER_MODE') == 'night-card':
    for path,template in [('internal/mcpserver/semantic_card_test.go','night_card_test.go.txt'),
                          ('internal/storage/semantic_card.go','night_card.go.txt'),
                          ('internal/notify/semantic_card.go','night_card_render.go.txt')]:
        overlay[str(root/path)]=str(source/template)
# Patch only the disposable build. Both paths keep the setting check and
# the existing DTO hash. Capture the baseline SQL directly for EXPLAIN.
getter=root/'internal/storage/sleep_duration_comparison.go'
src=getter.read_text()
begin='\terr = s.pool.QueryRow(readCtx, `'
end='\tpayload, _ := json.Marshal'
assert src.count(begin)==src.count(end)==1
lo,hi=src.index(begin),src.index(end)
original=src[lo:hi]
base_sql=original.split('`',2)[1]
branch="""	if SemanticBenchReplacement.Load() {
		SemanticBenchReplacementReads.Add(1)
		if err := s.semanticBenchRead(readCtx, &fact, &n, &historyVersion); err != nil {
			return &fact
		}
	} else {
		SemanticBenchBaseReads.Add(1)
"""
patched_getter=lab/'getter.go'
patched_getter.write_text(src[:lo]+branch+original+'\t}\n'+src[hi:])
helper=lab/'replacement.go'
helper.write_text((source/'replacement.go.txt').read_text()+'\nconst SemanticBenchBaseSQL = `'+base_sql+'`\n')
overlay[str(getter)]=str(patched_getter)
overlay[str(root/'internal/storage/semantic_bench.go')]=str(helper)
(lab/'overlay.json').write_text(json.dumps({'Replace':overlay}))
PYOVERLAY
go test -overlay "$lab/overlay.json" ./internal/mcpserver -run '^TestAccessMCPIsolatedTenantRoutingIntegration$' -count=1 -v -timeout=12m > "$lab/test.log" || { rg -m 1 -A35 "panic:|fatal error:|[.]go:[0-9]+: " "$lab/test.log" || tail -25 "$lab/test.log"; exit 1; }
rg -- '--- (PASS|FAIL)|^ok' "$lab/test.log"
mkdir -p "$pilot/artifacts"
if [[ "$SEMANTIC_READER_MODE" == night-card ]]; then
  install -m 600 "$lab/semantic-performance.json" "$pilot/artifacts/$artifact"
  exit 0
fi
# Vitest resolves the real UI imports from the feature directory. The file
# is created exclusively and removed by the trap after the dynamic test.
client_test=$(mktemp "$root/apps/web/src/features/sleep/SemanticReader.XXXXXXXX")
mv "$client_test" "$client_test.test.tsx"
client_test="$client_test.test.tsx"
cat "$SEMANTIC_PERF_SRC/SleepReady.test.tsx.txt" > "$client_test"
(cd apps/web && ./node_modules/.bin/vitest run "$client_test")
install -m 600 "$lab/semantic-performance.json" "$pilot/artifacts/$artifact"
