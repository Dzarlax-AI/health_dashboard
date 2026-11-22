#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$repo_root"
pg_bin="${STAGE_ONSET_PG_BIN:-$(pg_config --bindir)}"
lab="$(mktemp -d "${TMPDIR:-/tmp}/stage-onset.XXXXXX")"
port="55490"
cleanup() {
  "$pg_bin/pg_ctl" -D "$lab/data" -m immediate -w stop >/dev/null 2>&1 || true
  rm -rf "$lab"
}
trap cleanup EXIT

"$pg_bin/initdb" -D "$lab/data" -U "$(id -un)" --auth-local=trust --auth-host=trust --no-locale -E UTF8 >/dev/null
"$pg_bin/pg_ctl" -D "$lab/data" -l "$lab/postgres.log" -o "-k $lab -p $port -h '' -c shared_memory_type=mmap -c dynamic_shared_memory_type=mmap" -w start >/dev/null
export PGHOST="$lab" PGPORT="$port" PGUSER="$(id -un)" PGDATABASE=postgres
export PGOPTIONS="-c search_path=synthetic,public"
psql -X -v ON_ERROR_STOP=1 -qAt -f experiments/dbt_metrics/semantic/stage_onset_coverage_fixture.sql >/dev/null

python3 -B - "$repo_root" <<'PY'
import json
from datetime import datetime
import subprocess
import sys
from pathlib import Path

root = Path(sys.argv[1])
sys.path.insert(0, str(root / "experiments/dbt_metrics/scripts"))
from render_stage_onset_summary import stage_rows_query

rows_sql = stage_rows_query()
summary_sql = (root / "experiments/dbt_metrics/models/research_stage_onset_summary_v1.sql").read_text()
summary_sql = summary_sql.replace("{{ ref('research_stage_onset_rows_v1') }}", "research_stage_onset_rows_v1")
if "{{ ref(" in summary_sql:
    raise SystemExit("unresolved dbt ref in summary SQL")
script = f"""CREATE TEMP VIEW research_stage_onset_rows_v1 AS
{rows_sql};
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
\\echo __ROWS__
SELECT row_to_json(r)::text FROM research_stage_onset_rows_v1 r ORDER BY day;
\\echo __SUMMARY__
{summary_sql};
COMMIT;
"""
output = subprocess.check_output(["psql", "-X", "-v", "ON_ERROR_STOP=1", "-qAt"], input=script,
                                 text=True, cwd=root)
lines = [line for line in output.splitlines() if line]
try:
    row_marker = lines.index("__ROWS__")
    summary_marker = lines.index("__SUMMARY__")
except ValueError as exc:
    raise SystemExit(f"missing output markers: {output}") from exc
rows = [json.loads(line) for line in lines[row_marker + 1:summary_marker]]
summary = json.loads(lines[summary_marker + 1])
by_day = {row["day"]: row for row in rows}
expected = {
    "2026-11-01": ("candidate_proxy", True),
    "2026-11-02": ("candidate_proxy", True),  # Main night outranks the daytime nap.
    "2026-11-03": ("candidate_proxy", True),  # Internal wake does not split the night.
    "2026-11-04": ("ambiguous_session", False),  # Near-tied split sessions.
    "2026-11-05": ("candidate_proxy", False),  # Source epoch/generation switched.
    "2026-11-06": ("night_ineligible", True),  # Partial canonical night stays ineligible.
    "2026-11-07": ("awake_at_boundary", True),
    "2026-11-08": ("overlapping_labels", True),
    "2026-11-09": ("candidate_proxy", True),
    "2026-11-10": ("no_selected_stages", False),  # XML totals alone are not direct stages.
    "2026-11-11": ("candidate_proxy", False),  # Different source, same nominal epoch.
    "2026-11-12": ("candidate_proxy", True),
}
if set(by_day) != set(expected):
    raise SystemExit(f"row dates {sorted(by_day)} != expected {sorted(expected)}")
for day, (reason, bound) in expected.items():
    row = by_day[day]
    got = (row["reason"], row["canonical_binding"])
    if got != (reason, bound):
        raise SystemExit(f"{day}: got {got}, expected {(reason, bound)}: {row}")
    print(f"PASS {day}: {got}")

# Check selected episode timing explicitly: the nap is not chosen as onset or
# last sleep, while the internal wake is bridged and the later asleep end wins.
timing_expectations = {
    "2026-11-02": ("2026-11-01T22:00:00+01:00", "2026-11-02T06:00:00+01:00"),
    "2026-11-03": ("2026-11-02T22:00:00+01:00", "2026-11-03T06:20:00+01:00"),
}
for day, timing in timing_expectations.items():
    got = (by_day[day]["onset"], by_day[day]["asleep_end"])
    if tuple(map(datetime.fromisoformat, got)) != tuple(map(datetime.fromisoformat, timing)):
        raise SystemExit(f"{day}: selected timing {got}, expected {timing}")
print("PASS nap and internal-wake onset/end selection")

# The paired check-in must follow the selected asleep end; an earlier morning
# answer is excluded from after_sleep_label even though it is within the day.
if by_day["2026-11-01"]["after_sleep_label"] != "positive":
    raise SystemExit("post-sleep check-in was not selected")
if by_day["2026-11-09"]["after_sleep_label"] is not None:
    raise SystemExit("pre-sleep-end check-in was incorrectly paired")

if summary["calendar_days"] != 12 or summary["candidate_onset"] != 7 or summary["strict_bound_onset"] != 5:
    raise SystemExit(f"unexpected aggregate counts: {summary}")
if summary["candidate_score_pair"] != 6 or summary["candidate_sick_pair"] != 0:
    raise SystemExit(f"unexpected post-sleep workout/check-in pairs: {summary}")
epochs = summary["epochs"]
if len(epochs) != 2 or sorted(e["nights"] for e in epochs) != [1, 5] or sorted(e["strict_nights"] for e in epochs) != [0, 4]:
    raise SystemExit(f"source+epoch aggregation did not keep cohorts separate: {epochs}")
small, large = sorted(epochs, key=lambda e: e["nights"])
if small["sd_hours"] is not None or large["sd_hours"] is None:
    raise SystemExit(f"epoch statistics must be suppressed below five and emitted at five: {epochs}")
if summary["duration_label_proxy_end"] != 6 or summary["duration_label_calendar_only"] != 3:
    raise SystemExit(f"duration/check-in label cohorts changed: {summary}")
print("PASS aggregate counts, source+epoch separation, and 5-night suppression threshold")
PY

if [[ "${STAGE_ONSET_CHECK_DBT:-}" == "1" ]]; then
  bash experiments/dbt_metrics/scripts/check_stage_onset_dbt.sh 2026-11-01 2026-11-12
fi
