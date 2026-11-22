#!/usr/bin/env bash
# Called by the disposable stage-onset fixture runner after its direct SQL checks.
set -euo pipefail
pilot=$(cd "$(dirname "$0")/.." && pwd)
dbt=${STAGE_ONSET_DBT:-"$pilot/.venv/bin/dbt"}
path=${3:-stage}
case "$path" in
  stage|packet) ;;
  *) echo "expected stage or packet" >&2; exit 2 ;;
esac
lab=${PGHOST:?expected private fixture socket}
[[ -d "$lab" && -S "$lab/.s.PGSQL.$PGPORT" && -O "$lab" ]]
[[ ${PGDATABASE:?} == postgres ]]
test -s "$lab/data/postmaster.pid"
[[ "$(psql -XAtqc 'show data_directory')" -ef "$lab/data" ]]
test "$(psql -XAtqc 'show listen_addresses')" = ""
psql -Xq -v ON_ERROR_STOP=1 -v from_date="$1" -v through_date="$2" <<'SQL'
SELECT 1 / ((count(*) = 1 AND bool_and(start_date=:'from_date'::date
  AND end_date=:'through_date'::date AND tz='Europe/Belgrade')) IS TRUE)::int
FROM synthetic.research_parameters;
CREATE ROLE stage_fixture_dbt LOGIN;
CREATE SCHEMA dbt_output AUTHORIZATION stage_fixture_dbt;
GRANT USAGE ON SCHEMA synthetic TO stage_fixture_dbt;
GRANT SELECT ON ALL TABLES IN SCHEMA synthetic TO stage_fixture_dbt;
SQL

cat > "$lab/profiles.yml" <<'PROFILE'
health_dbt_metrics_pilot:
  target: fixture
  outputs:
    fixture:
      type: postgres
      host: "{{ env_var('PGHOST') }}"
      port: "{{ env_var('PGPORT') | int }}"
      user: stage_fixture_dbt
      password: synthetic-only
      dbname: postgres
      schema: dbt_output
      search_path: synthetic,pg_catalog
      threads: 1
PROFILE
DBT_SEND_ANONYMOUS_USAGE_STATS=false "$dbt" build --project-dir "$pilot" \
  --profiles-dir "$lab" --target-path "$lab/dbt-target" --log-path "$lab/dbt-logs" \
  --vars "{${path}_onset_research: true}" --select "+research_${path}_onset_summary_v1"

# Compare the guarded production SELECT with the dbt output on synthetic inputs.
python3 - "$pilot/scripts/render_stage_onset_summary.py" "$1" "$2" "$path" <<'PYCODE'
import json
import subprocess
import sys

renderer, first, last, path = sys.argv[1:]
query = subprocess.check_output([sys.executable, renderer] + (["--packet"] if path == "packet" else []), text=True)
psql = ['psql', '-XqAt', '-v', 'ON_ERROR_STOP=1']
result = subprocess.check_output(
    [*psql, '-v', f'start_date={first}', '-v', f'end_date={last}', '-v', 'tz=Europe/Belgrade'],
    input=f"BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;\n{query}\nROLLBACK;",
    text=True,
)
published = subprocess.check_output(
    [*psql, '-c', f"SELECT summary FROM dbt_output.research_{path}_onset_summary_v1 "
                  "WHERE to_regclass('dbt_output.research_stage_onset_rows_v1') IS NULL "
                  "AND to_regclass('dbt_output.research_packet_onset_rows_v1') IS NULL "
                  "AND to_regclass('dbt_output.research_packet_onset_base_v1') IS NULL"],
    text=True,
)
if not published.strip() or json.loads(result) != json.loads(published):
    sys.exit('dbt aggregate differs from direct SQL or intermediate rows were persisted')
if path == "packet":
    values = json.loads(result)
    expected = {"old_pairs": 3, "packet_pairs": 3, "both_paired": 2, "union_paired_dates": 4,
                "excluded_paired_dates": 1, "usable_paired_dates": 3, "usable_new_dates": 1}
    for key, count in expected.items():
        if values[key] != count:
            sys.exit(f"synthetic overlap check: {key}={values[key]}, expected {count}")
    provenance = {group["packet_provenance"]: group for group in values["packet_provenance"]}
    expected_tiers = {"journal_bound": (3, 3), "legacy_content_equivalent": (1, 0)}
    actual_tiers = {name: (group["onsets"], group["paired"]) for name, group in provenance.items()}
    if actual_tiers != expected_tiers:
        sys.exit(f"unexpected provenance counts in the overlap fixture: {actual_tiers}")
    print('PASS provenance tiers, overlap, deduplication and conflicting-onset exclusion')
print('PASS dbt aggregate parity and ephemeral intermediate')
PYCODE
