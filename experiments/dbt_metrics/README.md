# Synthetic dbt metrics pilot

This directory is a local experiment for the existing hourly and daily cache writers. It reads only generated synthetic rows in a disposable PostgreSQL 17.11 container and never reads `DATABASE_URL`, tenant data, or service configuration. It does not alter the application database or product deployment.

## Run the pilot

From the repository root, run the guarded driver with Python 3.12 and Docker available:

```bash
/Users/dzarlax/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python3 experiments/dbt_metrics/scripts/run_pilot.py
```

The driver installs the pinned dbt runtime into this directory's ignored `.venv`, creates a uniquely named container from the pinned PostgreSQL image digest, and binds its random host port to `127.0.0.1`. It writes an identity token in the database and a matching Docker label, checks both before database operations, and removes only that container ID and its anonymous volume when both markers still match. The database role used by dbt can read `source_data` and write `dbt_output`; it cannot write the source schema or read the Go oracle schema.

The driver loads `fixtures/schema.sql` and `fixtures/base.sql`, runs the existing Go aggregation functions through the opt-in `TestDBTMetricsPilot` harness, runs dbt models and data tests, and asserts both results against `fixtures/expected.json`. It then exercises repeat refresh, late old-date insertion, point update, staged-to-coarse sleep, incomplete-after-complete sleep, invalid quality, untouched dates, and the known stale-hour difference. Finally it creates a one-year synthetic scale set, runs three full and three single-old-date trials, and saves EXPLAIN ANALYZE/BUFFERS output.

Generated output is ignored under `artifacts/`, `target/`, `logs/`, `.venv/`, and `profiles.yml`. `artifacts/run-summary.json` contains the run results; `artifacts/oracle.json` contains the latest Go harness output. The summary is evidence from this synthetic fixture only.

## Incremental contract

The driver invokes dbt with explicit changed calendar days. The invocation shape is:

```bash
dbt run --select hourly_metrics daily_metrics \
  --vars '{"affected_dates":["2026-01-03"]}' \
  --project-dir <pilot-dir> --profiles-dir <pilot-dir>
dbt test --project-dir <pilot-dir> --profiles-dir <pilot-dir>
```

The one-command runner owns the disposable database lifecycle and cleans it up when it exits.

The incremental models reject a missing date list, malformed or non-calendar date, and dates absent from `source_data.calendar`. Hourly updates delete and rebuild each selected entire day so stale hourly rows disappear. Daily updates merge those selected days and preserve prior non-sleep fields when the new aggregate is null; the sleep block preserves all prior sleep fields when the picked source is incomplete, and clears stage fields when a complete coarse-only source replaces a staged result. A `--full-refresh` rebuild has no prior row to preserve, so some incremental results are history-dependent by design.

Full refresh always rebuilds the complete calendar. Combining `--full-refresh` with `affected_dates` is rejected to prevent replacing the entire history with a partial date selection.

## Semantics and limits

- Day/hour keys retain the Go writers' `SUBSTRING(date, ...)` behavior; timezone offsets are not normalized.
- Hourly averages use positive `quality='ok'` points. SUM metrics take the maximum inside each minute before summing minutes. Sleep-prefixed metrics follow the same SUM path and prefer a valid midnight summary over fragments for the same metric/day/source.
- Daily averages combine persisted hourly REAL values. Non-sleep sums prefer Apple Watch, then iPhone, then the maximum source total. Sleep source selection uses the `sleep_total` 1-hour floor and 1.4× disagreement check, deterministic `source ASC` ties, and the Go completeness gate.
- Go incremental upserts can retain stale non-sleep hourly rows when every source point becomes invalid. The dbt day replacement intentionally removes those rows; the runner asserts and reports this difference. A full rebuild restores parity.
- Benchmark times include fresh Go/dbt process startup for each invocation; Go also reports time spent inside the existing aggregate writers. The workload has 17,520 generated points plus 122 fixture and mutation rows across 380 dates. Timings are local, warm/uncontrolled-cache measurements with no production-load or speedup claim. EXPLAIN output covers compiled SQL only and excludes dbt DDL and hooks.

The pilot is not wired into application code, tenant schema contracts, migrations, production credentials, or production workflows.

## Go refactor acceptance

For behavior-preserving refactors, `scripts/run_go_contract.py --record`
records the current Go implementation before editing; `--compare` verifies
the candidate against that ignored baseline without overwriting it. Each run
owns and removes a separate synthetic container. Run one driver at a time.

The contract runner covers live, legacy force and legacy incremental writers,
including unequal source hours, unknown metrics, repeat refresh, late data,
quality invalidation and clean rebuild. It compares full hourly and selected
daily data fields, excludes `computed_at` and timing, and retains the candidate
aggregation contract version/checksum in each snapshot. The main dbt pilot
continues to exercise staged/coarse/incomplete sleep transitions and the known
Go/dbt differences. See [calculation update workflow](../../docs/CALCULATION_UPDATES.md).
