#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$repo_root"
pg_bin="${WORKOUT_COVERAGE_PG_BIN:-$(pg_config --bindir)}"
lab="$(mktemp -d "${TMPDIR:-/tmp}/workout-coverage.XXXXXX")"
port="55489"
cleanup() {
  "$pg_bin/pg_ctl" -D "$lab/data" -m immediate -w stop >/dev/null 2>&1 || true
  rm -rf "$lab"
}
trap cleanup EXIT

"$pg_bin/initdb" -D "$lab/data" -U "$(id -un)" --auth-local=trust --auth-host=trust --no-locale -E UTF8 >/dev/null
"$pg_bin/pg_ctl" -D "$lab/data" -l "$lab/postgres.log" -o "-k $lab -p $port -h '' -c shared_memory_type=mmap -c dynamic_shared_memory_type=mmap" -w start >/dev/null
export PGHOST="$lab" PGPORT="$port" PGUSER="$(id -un)" PGDATABASE=postgres
psql -X -v ON_ERROR_STOP=1 -qAt <<'SQL'
CREATE SCHEMA synthetic;
SET search_path=synthetic,public;
\i experiments/dbt_metrics/semantic/workout_sleep_coverage_fixture.sql
SQL

python3 - "$repo_root" <<'PY'
import json
import os
import subprocess
import sys

root = sys.argv[1]
query = "experiments/dbt_metrics/semantic/workout_sleep_coverage.sql"
expected = {
    "2026-06-02": (1, 1, 0, 0),  # Belgrade local date differs from UTC date.
    "2026-06-03": (1, 0, 1, 0),  # overlapping workout cannot count as prior.
    "2026-06-04": (0, 0, 0, 0),  # sleep device does not match night source.
    "2026-06-05": (0, 0, 0, 0),  # ambiguous duplicate sleep episodes.
    "2026-06-06": (0, 0, 0, 0),  # no exact episode start timestamp.
    "2026-06-07": (1, 1, 0, 1),  # check-in was before sleep ended.
    "2026-06-08": (1, 1, 0, 0),  # late answer is accepted, but not on-time.
    "2026-06-09": (1, 1, 0, 1),  # null answer is not an answered check-in.
}
for day, (onsets, workouts, no_workout, no_answer) in expected.items():
    cmd = ["psql", "-X", "-v", "ON_ERROR_STOP=1", "-v", f"start_date={day}",
           "-v", f"end_date={day}", "-v", "tz=Europe/Belgrade", "-qAt"]
    sql = f"SET search_path=synthetic,public;\nBEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;\n\\i {query}\nCOMMIT;\n"
    raw = subprocess.check_output(cmd, input=sql, text=True, cwd=root).strip()
    result = json.loads(raw)
    got = (result["supported_onset_proxies"], result["supported_onset_and_prior_workout"],
           result["no_qualifying_workout"], result["no_subsequent_answer"],
           result["eligible_with_on_time_checkin"], result["eligible_with_late_only_checkin"])
    want = (onsets, workouts, no_workout, no_answer, int(day == "2026-06-02"), int(day == "2026-06-08"))
    if got != want:
        raise SystemExit(f"{day}: got {got}, expected {want}; {result}")
    if day == "2026-06-02":
        epochs = result["epochs"]
        if len(epochs) != 1 or epochs[0]["nights"] != 1 or any(
            epochs[0][key] is not None for key in ("min_hours", "max_hours", "mean_hours", "stddev_hours")
        ):
            raise SystemExit(f"small epoch must retain count and suppress interval statistics: {epochs}")
    print(f"PASS {day}: {got}")
print("PASS isolated synthetic workout/sleep coverage regression")
PY
