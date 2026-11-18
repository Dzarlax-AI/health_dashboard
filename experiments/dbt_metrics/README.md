# Synthetic dbt metrics pilot

This directory is a local experiment for the existing hourly and daily cache writers. It reads only generated synthetic rows in a disposable PostgreSQL 17.11 container and never reads `DATABASE_URL`, tenant data, or service configuration. It does not alter the application database or product deployment.

## Run the pilot

From the repository root, run the guarded driver with Python 3.12 and Docker available:

```bash
python3 experiments/dbt_metrics/scripts/run_pilot.py
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

The hourly and daily experiments are independent of application runtime, tenant schema migrations, and production workflows.

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

## Rust dbt v2 compatibility preflight (2026-10-05)

The official `dbt==2.0.6` binary was installed in a separate temporary virtual
environment, preserving the pinned v1 pilot. Reproduce the adapter preflight:

```bash
python3 experiments/dbt_metrics/scripts/run_v2_smoke.py --dbt /path/to/v2/dbt
```

With synthetic credentials and loopback port 1, `dbt debug` rejects `type: postgres`
before connecting: `dbt1005`, "The 'postgres' adapter is not yet supported by dbt."
The ignored `artifacts/v2-smoke.json` records the binary version and diagnostic.
No SQL benchmark ran; the configuration failure duration is not an aggregation
timing. Experimental adapters were not enabled.

[dbt v2 GA](https://docs.getdbt.com/blog/dbt-v2-is-ga) describes the Rust engine;
the [supported platforms](https://docs.getdbt.com/docs/supported-data-platforms)
and [Postgres adapter request](https://github.com/dbt-labs/dbt-core/issues/13123)
explain the compatibility boundary. For this PostgreSQL pilot, retain v1 until
a supported v2 adapter can execute the same fixtures and parity checks.


## Main-night duration reference

Run the opt-in view build and the Go parity suite with local PostgreSQL 16 or
17 binaries and the existing dbt virtual environment:

```bash
SLEEP_REFERENCE_PG_BIN=/path/to/postgresql/bin \
  experiments/dbt_metrics/scripts/run_sleep_reference.sh
```

The runner owns a disposable loopback cluster and uses the unchanged
`SelectMorningReportSleep` as its oracle. It checks dated snapshots, compares
dbt’s catalog definition with the same macro rendered by Go, and records the
single-date index plans. In the application, tenant schema migration v13
installs that SQL as a security-invoker view. The per-tenant
`sleep_duration_comparison_enabled` setting is off unless set to `true`.

The dbt Charts source reads 30 typed facts exported by
`TestSleepDurationComparisonSnapshots`. Against an explicitly disposable local
test cluster, set `SLEEP_REFERENCE_CHART_EXPORT` to
`$PWD/experiments/dbt_metrics/artifacts/sleep_comparison.json` when running that
test. Then validate and render with dbt Charts 0.11.1:

```bash
cd experiments/dbt_metrics
export DO_NOT_TRACK=1
dct validate charts/sleep-comparison.yml --project-dir .
dct render charts/sleep-comparison.yml --project-dir . --no-cache \
  --format html --output artifacts/sleep-comparison.html
```

## Local sleep and HRV semantic shadow

The [metric catalog](semantic/catalog.md) describes the supported observation keys and derived definitions. The shadow consumes [one profile per input schema](semantic/inputs.sql). The evaluation calendar defines the denominator. Load earlier nights for the Go reference window and adjacent HRV/workouts for boundary checks. HRV compatibility uses the writer's stored local date. Candidate interval joins compare absolute instants and keep source-specific means.

Run from the repository root with PostgreSQL 17 server tools, the project's dbt Python environment and Go dependencies available locally.

```sh
python3 experiments/dbt_metrics/scripts/check_semantic_catalog.py
experiments/dbt_metrics/.venv/bin/python experiments/dbt_metrics/scripts/run_semantic_shadow.py \
  --pg-bin /opt/homebrew/opt/postgresql@17/bin \
  --output experiments/dbt_metrics/artifacts/semantic-synthetic.json
```

The driver creates an owner-only PostgreSQL cluster listening on a private Unix socket. It builds the views, tests invoker permissions and role isolation, compares against Go and discards the cluster. The synthetic run also exercises conflicting stages, history corrections, method, time precision, DST and adjacent workouts. `--private-extract` accepts an existing owner-only NDJSON extract from the [bounded profile query](semantic/profile_select.sql). Production access is separate from this driver. That query is date-specific and requires an already verified tenant role in a read-only snapshot. The output JSON contains aggregated provenance and coverage. Keep it owner-readable under the ignored artifacts directory.

The sleep adapter groups the chosen source's non-midnight stage fragments into sessions separated by gaps over 30 minutes. It admits a unique longest session ending on the wake date when unioned asleep time matches the complete plausible canonical night within one minute. Midnight stage summaries, period sleep episodes and noon-to-noon capture attestations have distinct meanings. Conflicting stage labels are exposed. This is a conservative reconstructed episode, and the source row has no authoritative main-session ID.

The generic persisted HRV key and a device name cannot establish a method. The extract retains the observation channel separately. Timestamp comparisons use the recorded instant and never establish a measurement interval. Post-workout uses a separate exploratory 30-minute window and excludes time spent in another active workout. The SQL cases cover known source type, unknown method, bucket precision, overlapping workouts and conflicting repeated measurements. Thresholds for publishing new facts remain open.

For the recovered local cohort, add `--sleep-recovery /private/owner-only/packets.ndjson` alongside `--private-extract` and choose a different output path. The [bounded replay extract](semantic/sleep_replay_select.sql) contains only night commitments, points and the selected fields from source packets. The driver replays eligible packets through the Go parser and transactional writer in its disposable cluster, repeats the replay to verify idempotency, reconciles every baseline night with the semantic extract, then reruns the full shadow and Go oracles. The raw files can be discarded after verification.

To compare the two readers on the same two synthetic tenant profiles, run:

```sh
SEMANTIC_PG_BIN=/opt/homebrew/opt/postgresql@17/bin \
  bash experiments/dbt_metrics/scripts/run_semantic_reader_bench.sh --replace
```

The replacement benchmark uses a Go build overlay at the shared sleep comparison reader. Both arms use the same feature flag, pool and response hashing. One arm reads the existing reference and formats the result in Go. The other selects the comparison fields from the semantic sleep-day view and joins the canonical night for its observation time in the same statement. The run compares complete HTTP comparison responses over the 90-day calendar and one absent date per profile, then alternates the two readers on the dated Sleep page, MCP tool and local morning report. Each timed batch checks the exclusive reader counts and records profile-specific latency. The output is `artifacts/semantic-replacement-performance.json`. Running without `--replace` reproduces the earlier additional-query diagnostic.

Use `--mcp-narrow` to run only the authenticated MCP tool with the existing reader, the wide sleep-day view and the [duration comparison view](models/semantic_sleep_duration_comparison_v1.sql) in the same local run. The narrow view has one row per canonical night, with the same historical reference, classification and minute rounding as the existing comparison. Only the dated night and its qualifying history are needed. This mode checks the complete MCP response across all 150 fixture nights and one absent date per profile, exercises all six reader orders, and checks the narrow execution plan reads only the canonical night table. Results are written to `artifacts/semantic-mcp-narrow-performance.json`.

Use `--mcp-cache` with the same local script to compare the current MCP reader, the narrow live view and an indexed [incremental dbt table](models/semantic_sleep_duration_cache_v1.sql). The driver runs dbt as each fixture tenant. The model is opt-in with `semantic_sleep_cache`, requires the narrow view in the target schema, and takes `semantic_source_schema` for the canonical input. Keep the source and target inside the same tenant when running it as in this benchmark.

The first build fills the table. Later builds compare canonical input fields with the saved rows, including deleted or backdated nights. They rebuild existing wake dates from each changed date through 30 days later, inclusive. Removal of obsolete rows and replacement of dependent rows commit together. A run with identical inputs writes zero fact rows. This implementation scans input keys and comparison fields to detect changes. A formula change requires a full refresh. Between builds readers see the previous committed generation.

The local driver measures complete MCP reads, then commits a historical edit and starts a fresh dbt CLI process. It polls the MCP tool every 25 ms until the full response matches live computation. The JSON records this observed lag separately from model and process duration. The tests also cover the last affected day, unchanged surrounding rows, a repeated build, deletion and late insertion. Results are written to `artifacts/semantic-mcp-cache-performance.json`.

The local main-night card includes stage minutes and shares, evidence for internal awakenings, and the existing Go sleep-quality factors applied to the inputs attributed to that night. The temporal count requires a continuous detailed episode. Confirmed bouts in interrupted observations can still provide a lower bound. Daily sums cannot supply timing. The `quality_basis` and `quality_awake_basis` fields explain component admission and explicit versus inferred zero. Comparing scores from the historical Dashboard requires the same dated inputs and confidence evidence.

Run the paired authenticated tool and morning report harness with `--night-card` on `run_semantic_reader_bench.sh`. The model `semantic_sleep_night_card_cache_v1` compares the full evaluated calendar to its saved rows on refresh, so stage-only edits, moving an episode and late historical inputs are reflected even with unchanged canonical duration. Only changed or removed cards cause writes. For a coverage report and private offline readback, run `run_semantic_shadow.py` with `--night-card-only` and optionally `--night-card-html PATH`. `extract_semantic_sleep_archive.py ARCHIVE NEW_DIRECTORY --as-of ISO_TIMESTAMP` recreates the two minimized inputs for `--private-extract` and `--sleep-recovery` from the local primary-profile archive using PostgreSQL 17 tools. Raw personal exports and dated readbacks belong in an owner-only directory. The existing duration-only cache remains independently queryable.
