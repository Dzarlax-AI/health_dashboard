#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
cd "$repo_root"
pg_bin="${STAGE_ONSET_PG_BIN:-$(pg_config --bindir)}"
lab="$(mktemp -d "${TMPDIR:-/tmp}/raw-packet-onset.XXXXXX")"
port="${STAGE_ONSET_TEST_PORT:-55492}"
cleanup() {
  "$pg_bin/pg_ctl" -D "$lab/data" -m immediate -w stop >/dev/null 2>&1 || true
  rm -rf "$lab"
}
trap cleanup EXIT

"$pg_bin/initdb" -D "$lab/data" -U "$(id -un)" --auth-local=trust --auth-host=trust --no-locale -E UTF8 >/dev/null
"$pg_bin/pg_ctl" -D "$lab/data" -l "$lab/postgres.log" \
  -o "-k $lab -p $port -h '' -c shared_memory_type=mmap -c dynamic_shared_memory_type=mmap" -w start >/dev/null
export PGHOST="$lab" PGPORT="$port" PGUSER="$(id -un)" PGDATABASE=postgres
export PGOPTIONS="-c search_path=synthetic,public -c timezone=UTC"
psql -X -v ON_ERROR_STOP=1 -qAt -f experiments/dbt_metrics/semantic/raw_packet_onset_fixture.sql >/dev/null

python3 - "$repo_root" <<'PY'
import json
import subprocess
import sys
from pathlib import Path

root = Path(sys.argv[1])
model = (root / "experiments/dbt_metrics/models/research_packet_onset_base_v1.sql").read_text()
query = f"""BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
WITH research_parameters AS (
 SELECT '2026-11-01'::date start_date,'2026-11-30'::date end_date,'Europe/Belgrade'::text tz
)
SELECT row_to_json(r)::text FROM ({model}) r ORDER BY day;
COMMIT;"""
output = subprocess.check_output(["psql", "-X", "-v", "ON_ERROR_STOP=1", "-qAt"],
                                 input=query, text=True, cwd=root)
rows = [json.loads(line) for line in output.splitlines() if line.startswith("{")]
by_day = {row["day"]: row for row in rows}
expected = {
    "2026-11-01": "candidate_proxy",
    "2026-11-02": "candidate_proxy",
    "2026-11-03": "overlapping_labels",
    "2026-11-04": "wake_date_mismatch",
    "2026-11-05": "duration_mismatch",
    "2026-11-06": "no_standalone_stages",
    "2026-11-07": "no_standalone_stages",
    "2026-11-08": "no_standalone_stages",
    "2026-11-09": "no_standalone_stages",
    "2026-11-10": "unbound_current_packet",
    "2026-11-11": "unbound_current_packet",
    "2026-11-12": "invalid_stage_sample",
    "2026-11-13": "coverage_boundary_crossing",
    "2026-11-14": "candidate_proxy",
    "2026-11-15": "awake_at_boundary",
    "2026-11-16": "invalid_packet",
    "2026-11-17": "no_standalone_stages",
    "2026-11-18": "unbound_current_packet",
    "2026-11-19": "invalid_stage_sample",
    "2026-11-20": "candidate_proxy",
    "2026-11-21": "unbound_current_packet",
    "2026-11-22": "fingerprint_unverified",
    "2026-11-23": "unbound_current_packet",
    "2026-11-24": "unbound_current_packet",
    "2026-11-25": "unbound_current_packet",
    "2026-11-26": "unbound_current_packet",
    "2026-11-27": "unbound_current_packet",
    "2026-11-28": "unbound_current_packet",
    "2026-11-29": "unbound_current_packet",
    "2026-11-30": "unbound_current_packet",
}
if len(rows) != 30 or not set(expected).issubset(by_day):
    raise SystemExit(f"expected 30 calendar rows and all cases; got {len(rows)}")
for day, reason in expected.items():
    got = by_day[day]["reason"]
    if got != reason:
        raise SystemExit(f"{day}: reason={got}, expected {reason}: {by_day[day]}")
    print(f"PASS {day}: {reason}")

for day, onset, end in [
    ("2026-11-01", "2026-10-31T21:00:00+00:00", "2026-11-01T05:00:00+00:00"),
    ("2026-11-02", "2026-11-01T21:00:00+00:00", "2026-11-02T05:00:00+00:00"),
    ("2026-11-14", "2026-11-13T21:00:00+00:00", "2026-11-14T04:30:00+00:00"),
]:
    actual = (by_day[day]["onset"], by_day[day]["asleep_end"])
    if actual != (onset, end):
        raise SystemExit(f"{day}: timing={actual}, expected {(onset, end)}")
print("PASS local timezone and corrected-owner onset/end selection")

for day in ("2026-11-01", "2026-11-02", "2026-11-14"):
    if by_day[day]["canonical_binding"] is not False:
        raise SystemExit(f"{day}: proxy must not claim strict canonical binding")
print("PASS onset proxies stay distinct from strict canonical binding")

if by_day["2026-11-20"]["packet_provenance"] != "legacy_content_equivalent":
    raise SystemExit(f"legacy fixture did not take content-equivalent path: {by_day['2026-11-20']}")
for day in ("2026-11-21", "2026-11-22", "2026-11-23", "2026-11-24", "2026-11-25",
            "2026-11-26", "2026-11-27", "2026-11-28", "2026-11-29", "2026-11-30"):
    if by_day[day]["packet_provenance"] is not None:
        raise SystemExit(f"{day}: invalid legacy packet received provenance {by_day[day]['packet_provenance']}")
print("PASS legacy content-equivalent proof and fail-closed controls")
PY

# The parent-owned overlay checks pairing against the previous snapshot lane.
psql -X -q -v ON_ERROR_STOP=1 -f experiments/dbt_metrics/semantic/packet_pairing_fixture.sql >/dev/null
if [[ "${STAGE_ONSET_CHECK_DBT:-}" == "1" ]]; then
  bash experiments/dbt_metrics/scripts/check_stage_onset_dbt.sh 2026-11-01 2026-11-20 packet
fi
if [[ "${STAGE_ONSET_CHECK_STATISTICS:-}" == "1" ]]; then
  bash experiments/dbt_metrics/semantic/test_onset_statistics.sh
fi
