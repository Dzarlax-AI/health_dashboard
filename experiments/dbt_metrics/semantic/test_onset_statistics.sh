#!/usr/bin/env bash
# Disposable Docker round for exact epoch median and leave-one-out thresholds.
set -euo pipefail

if (( $# > 1 )); then
  echo "usage: $0 [synthetic-report.html]" >&2
  exit 2
fi

repo_root="$(cd "$(dirname "$0")/../../.." && pwd)"
pilot="$repo_root/experiments/dbt_metrics"
container="$(docker run --rm -d --label health.synthetic.onset.stats=1 \
  -p 127.0.0.1::5432 --tmpfs /var/lib/postgresql/data \
  -e POSTGRES_PASSWORD=synthetic-only postgres:16-alpine)"
cleanup() { docker stop -t 1 "$container" >/dev/null 2>&1 || true; }
trap cleanup EXIT

mapping="$(docker port "$container" 5432/tcp)"
host="${mapping%:*}"
port="${mapping##*:}"
[[ "$host" == 127.0.0.1 ]]
export PGHOST="$host" PGPORT="$port" PGUSER=postgres PGDATABASE=postgres PGPASSWORD=synthetic-only
export PGOPTIONS="-c search_path=synthetic,public -c timezone=UTC"
for _ in $(seq 1 50); do
  if pg_isready >/dev/null 2>&1; then break; fi
  sleep 0.2
done
pg_isready >/dev/null

psql -Xq -v ON_ERROR_STOP=1 -f "$pilot/semantic/raw_packet_onset_fixture.sql" >/dev/null
psql -Xq -v ON_ERROR_STOP=1 -f "$pilot/semantic/packet_pairing_fixture.sql" >/dev/null
psql -Xq -v ON_ERROR_STOP=1 -f "$pilot/semantic/onset_statistics_fixture.sql" >/dev/null

python3 - "$pilot" "$@" <<'PY'
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

pilot = Path(sys.argv[1])
host, port = os.environ["PGHOST"], os.environ["PGPORT"]
dbt = pilot / ".venv/bin/dbt"
psql = ["psql", "-XqAt", "-v", "ON_ERROR_STOP=1"]
subprocess.run(psql, input=(
    "CREATE ROLE synthetic_dbt LOGIN PASSWORD 'synthetic-only'; "
    "CREATE SCHEMA dbt_output AUTHORIZATION synthetic_dbt; "
    "GRANT USAGE ON SCHEMA synthetic TO synthetic_dbt; "
    "GRANT SELECT ON ALL TABLES IN SCHEMA synthetic TO synthetic_dbt;"
), text=True, check=True)

with tempfile.TemporaryDirectory(prefix="onset-stats-dbt-") as temp:
    temp = Path(temp)
    profile = temp / "profiles.yml"
    profile.write_text(f"""health_dbt_metrics_pilot:
  target: fixture
  outputs:
    fixture:
      type: postgres
      host: {host}
      port: {port}
      user: synthetic_dbt
      password: synthetic-only
      dbname: postgres
      schema: dbt_output
      search_path: synthetic,pg_catalog
      threads: 1
""")
    subprocess.run([str(dbt), "build", "--project-dir", str(pilot),
        "--profiles-dir", str(temp), "--target-path", str(temp / "target"),
        "--log-path", str(temp / "logs"), "--vars", "{packet_onset_research: true}",
        "--select", "+research_packet_onset_summary_v1"], check=True,
        env={**os.environ, "DBT_SEND_ANONYMOUS_USAGE_STATS": "false"})

    renderer = pilot / "scripts/render_stage_onset_summary.py"
    rendered = subprocess.check_output([sys.executable, str(renderer), "--packet"], text=True)
    direct = subprocess.check_output([*psql, "-v", "start_date=2026-12-01",
        "-v", "end_date=2026-12-31", "-v", "tz=Europe/Belgrade"],
        input="BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;\n" + rendered + "\nROLLBACK;",
        text=True)
    published = subprocess.check_output([*psql, "-c", "SELECT summary FROM dbt_output.research_packet_onset_summary_v1 "+
        "WHERE to_regclass('dbt_output.research_stage_onset_rows_v1') IS NULL "+
        "AND to_regclass('dbt_output.research_packet_onset_rows_v1') IS NULL "+
        "AND to_regclass('dbt_output.research_packet_onset_base_v1') IS NULL"], text=True)
    direct_summary = json.loads(direct.strip())
    dbt_summary = json.loads(published.strip())
    if direct_summary != dbt_summary:
        raise SystemExit("dbt aggregate differs from direct SQL")

    epochs = direct_summary["epochs"]
    if len(epochs) != 3:
        raise SystemExit(f"expected 3 independent epochs, got {epochs}")
    by_counts = {(row["positive"], row["meh"]): row for row in epochs}
    if set(by_counts) != {(5, 5), (5, 6), (4, 6)}:
        raise SystemExit("unexpected group sizes in the synthetic fixture")
    expected = {
        (5, 5): (4.00, 2.00, 2.00, 0, None, None, None, None),
        (5, 6): (5.00, 2.25, 2.75, 6, 2.50, 3.00, 2.50, 3.00),
        (4, 6): (None, None, None, 0, None, None, None, None),
    }
    keys = ("median_positive_hours", "median_meh_hours", "median_diff_hours", "loo_count",
            "loo_mean_min_hours", "loo_mean_max_hours", "loo_median_min_hours", "loo_median_max_hours")
    for counts, row in by_counts.items():
        actual = tuple(row[key] for key in keys)
        if actual != expected[counts]:
            raise SystemExit(f"{counts}: got {actual}, expected {expected[counts]}: {row}")
        print(f"PASS epoch positive/meh={counts}: median and LOO thresholds")
print("PASS direct SQL/dbt parity for three isolated epochs")
if len(sys.argv) == 3:
    report = subprocess.check_output(
        [sys.executable, str(pilot / "scripts/render_workout_sleep_report.py"), "--synthetic"],
        input=json.dumps(dbt_summary), text=True,
    )
    with Path(sys.argv[2]).open("x", encoding="utf-8") as output:
        output.write(report)
    print("Created synthetic example report")
PY
