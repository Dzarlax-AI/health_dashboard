# Synthetic dbt metrics pilot

This directory is a local experiment for the existing hourly and daily cache writers. It reads only generated synthetic rows in a disposable PostgreSQL 17.11 container and never reads `DATABASE_URL`, tenant data, or service configuration. It does not alter the application database or product deployment.

## Run the complete graph in two local profiles

From the repository root, with Docker, Go and the pilot dbt environment in `.venv` under this directory:

```sh
python3 experiments/dbt_metrics/scripts/run_profile_graph.py
```

The driver creates a disposable PostgreSQL container from the pilot's pinned image. Each of two fixture roles builds all project models in its own schema using its own synthetic input tables. The manifest and the database must account for every model SQL file, including incremental relations. The driver runs every enabled dbt data test and checks that each role can access only its own profile schemas.

The bundled Go probes exercise the application’s daily and sleep reads against these installed relations, including switching back to the legacy readers. A final incremental build corrects one profile’s steps from 17 to 19 while the other profile retains 71 on the same day. It also checks every unrelated daily row and rejection of an unknown date. Historical inputs for decision-time models are synthetic captures in this installation.

A final HTTP probe checks a repeated clock hour and a measured zero in each
profile. On the fixture day, the first 02:00 contains 40 Watch steps at
UTC+02:00 and the second contains 70 phone steps at UTC+01:00. The dbt
daily value and both API contracts return 110. Correcting the phone to 74
changes the first profile to 114. The peer stays at 110, and both return an
explicit zero on the following date.

In the complete graph, `metric_read_v1` also projects the fields declared in
`health_metric_catalog.yml` directly from their dbt relations. Each row uses
the key declared for its producer, including the component for multicolumn
bindings. The driver checks all 123 bindings for sample rows and unique keys,
compares all dated step results with `daily_metrics`, and inspects the query
plan for a single dated lookup. Other catalog entries can receive observations
through the existing profile fact table.

## Run the pilot

From the repository root, run the guarded driver with Python 3.12 and Docker available:

```bash
python3 experiments/dbt_metrics/scripts/run_pilot.py
```

The driver installs the pinned dbt runtime into this directory's ignored `.venv`, creates a uniquely named container from the pinned PostgreSQL image digest, and binds its random host port to `127.0.0.1`. It writes an identity token in the database and a matching Docker label, checks both before database operations, and removes only that container ID and its anonymous volume when both markers still match. The database role used by dbt can read `source_data` and write `dbt_output`; it cannot write the source schema or read the Go oracle schema.

The driver loads `fixtures/schema.sql` and `fixtures/base.sql`, runs the existing Go aggregation functions through the opt-in `TestDBTMetricsPilot` harness, runs dbt models and data tests, and asserts both results against `fixtures/expected.json`. It then exercises repeat refresh, late old-date insertion, point update, staged-to-coarse sleep, incomplete-after-complete sleep, partial step retransmission, step source withdrawal and return, and untouched dates. Finally it creates a one-year synthetic scale set, runs three full and three single-old-date trials, and saves EXPLAIN ANALYZE/BUFFERS output.

Concurrent pilots use a private directory for dbt compilation, profiles, logs, and Go oracle output. A successful pilot publishes its results to `artifacts/run-summary.json`, `artifacts/oracle.json`, and `artifacts/explain-*.txt`. The summary is evidence from this synthetic fixture only.

## Incremental contract

The driver invokes dbt with explicit changed calendar days. The invocation shape is:

```bash
dbt run --select +daily_metrics \
  --vars '{"affected_dates":["2026-01-03"]}' \
  --project-dir <pilot-dir> --profiles-dir <pilot-dir>
dbt test --project-dir <pilot-dir> --profiles-dir <pilot-dir>
```

The default `aggregate_capture_mode: snapshot` reads the accumulated `source_data.metric_points` table, just as the server reads its stored inputs. A local offline feed can use `aggregate_capture_mode: partial` after an initial snapshot. It supplies accepted upserts in `source_data.metric_point_changes`, keyed by metric, original timestamp and source. The adapter merges those points into its existing history before rebuilding the selected dates. An empty batch changes no raw points. A clean build from a partial feed fails because its missing points cannot be recovered. The regular mobile ingest protocol already retains accepted points in the database.

The one-command runner owns the disposable database lifecycle and cleans it up when it exits.

The incremental models reject a missing date list, malformed or non-calendar date, and dates absent from `source_data.calendar`. Hourly updates rebuild the selected days. For step counts, `step_hour_state` keeps previously observed source hours across partial captures. Daily updates merge those selected days. Explicit invalidation of the last step source clears the step total. Other non-sleep fields retain prior values when a new aggregate is null. The sleep block preserves all prior sleep fields when the picked source is incomplete, and clears stage fields when a complete coarse-only source replaces a staged result. A `--full-refresh` rebuild has no prior row to preserve, so some incremental results are history-dependent by design.

Full refresh always rebuilds the complete calendar. Combining `--full-refresh` with `affected_dates` is rejected to prevent replacing the entire history with a partial date selection.

## Semantics and limits

- Day and hour labels use the stored local time. For steps, equivalent offset representations of a measurement with the same source, grain, quantity and UTC instant contribute once. A nonzero offset wins over UTC, followed by bytewise timestamp order for ties. Both occurrences of an autumn clock hour contribute when their UTC instants differ.
- Hourly averages use positive `quality='ok'` points. SUM metrics take the maximum inside each minute before summing minutes. Sleep-prefixed metrics follow the same SUM path and prefer a valid midnight summary over fragments for the same metric/day/source.
- Daily HRV, resting heart rate, oxygen saturation, VO₂ max, and respiratory rate average the hourly REAL values within each source, then weight those source means equally in foreground and historical writers. Non-sleep sums prefer Apple Watch, then iPhone, then the maximum source total. Sleep source selection uses the `sleep_total` 1-hour floor and 1.4× disagreement check, deterministic `source ASC` ties, and the Go completeness gate.
- For steps, explicit invalidation removes the withdrawn source’s hours. A partial retransmission preserves the other sources. When an import or admin edit confirms physical removal, pass `verified_step_withdrawals: true` and supply the exact withdrawn `point_date` and `source` in `source_data.verified_step_withdrawals` for that capture. The offline adapter retains the other minutes and sources when only one point changes. Replace the receipt set for the next capture. The synthetic driver supplies these receipts explicitly. A surviving positive point still contributes. An archive containing only current measurements cannot reconstruct earlier withdrawal receipts or the previous contents of a partially observed hour. Go incremental upserts for other non-sleep metrics can retain stale hourly rows if all points become invalid.
- Benchmark times include fresh Go/dbt process startup for each invocation; Go also reports time spent inside the existing aggregate writers. The workload has 17,520 generated points plus the fixture and mutation rows across 380 dates. Timings are local, warm/uncontrolled-cache measurements with no production-load or speedup claim. EXPLAIN output covers compiled SQL only and excludes dbt DDL and hooks.

The pilot runs in an isolated database. The Go service embeds the shared hourly aggregation, daily candidates and retained-value SQL. The legacy sleep selector remains reversible with its existing profile gate.

## Go refactor acceptance

For behavior-preserving refactors, `scripts/run_go_contract.py --record`
records the current Go implementation before editing; `--compare` verifies
the candidate against that ignored baseline without overwriting it. Each run
owns and removes a separate synthetic container.

The contract runner covers both live sleep implementations and the full, incremental, and date-bounded historical writers,
including unequal source hours, unknown metrics, repeat refresh, late data,
quality invalidation and clean rebuild. It compares full hourly and selected
daily data fields, excludes `computed_at` and timing, and retains the candidate
aggregation contract version/checksum in each snapshot. The main dbt pilot
also exercises staged/coarse/incomplete sleep transitions and explicit step withdrawals. See [calculation update workflow](../../docs/CALCULATION_UPDATES.md).

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

Pass `--docker` to run PostgreSQL 16 in a disposable container using the local PostgreSQL client tools. Both modes exercise the live comparison reader, Today and dated Sleep HTTP responses, and the sent morning report archive.

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

## Incremental HRV correction check

Run `python3 experiments/dbt_metrics/scripts/run_hrv_transition.py` with Docker, Go and the pilot dbt environment installed. The synthetic fixture compares the incremental writers and morning reader with the dbt observation and daily views. A correction from 60 to 70 ms updates both paths. Without a confirmed withdrawal, the morning comparison permits the provisional same-day cache. Once the withdrawal is recorded, both readers suppress the stale value even before the aggregate repair finishes. The storage tests exercise the actual withdrawal paths, restart, weighting and date isolation.

## Local sleep and HRV semantic shadow

The [dbt metric catalog](semantic/catalog.md) publishes the supported observation keys and derived definitions. Its query guide covers the PostgreSQL registry view and native MetricFlow sleep metrics. The shadow consumes [one profile per input schema](semantic/inputs.sql). The evaluation calendar defines the denominator. Load earlier nights for the Go reference window and adjacent HRV/workouts for boundary checks. HRV compatibility uses the writer's stored local date. Candidate interval joins compare absolute instants and keep source-specific means.

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

## Readiness HRV contribution

Run `python3 experiments/dbt_metrics/scripts/run_readiness_hrv_shadow.py` from the repository root with Docker, Go, and the pilot dbt environment installed. The runner creates a disposable PostgreSQL instance and uses synthetic data. The model is opt-in through `readiness_hrv_shadow`.

`semantic_readiness_hrv_component_v1` reports the selected observation dates and values, availability and its reason, personal baseline, and unrounded contribution to Readiness. A historical calculation uses dated raw means. The cache-backed morning calculation can use the same day's fresh mean over a cached value and uses eligible prior cached values. A pending repair suppresses that day's cached HRV. The capture records the actual reader path and same-day evidence gate. A missing morning capture is marked unrecoverable. A later correction has its own capture identity.

This pilot tests the component before normalization across HRV, resting heart rate, and sleep. The application still serves the Go calculation and applies its existing confidence limits. The synthetic capture table represents the inputs a future evaluation would need to retain.

### Local comparison of complex metrics

From the repository root, run the complete synthetic comparison with Go, Docker,
PostgreSQL client tools, Python 3 and the pilot dbt environment installed.

```sh
python3 experiments/dbt_metrics/scripts/run_complex_metrics.py
```

The [coverage matrix](semantic/complex_metric_coverage.md) identifies the actual Go
readers, dbt models, reproduced differences and historical inputs needed for each family.

### Unified local metric schema

Run the following from the repository root. The check needs Docker, Go, Python,
PostgreSQL client tools, the pilot dbt environment, and the sibling Health Sync
sources used by the ingress catalog check.

```sh
python3 experiments/dbt_metrics/metric_schema/run.py
python3 experiments/dbt_metrics/metric_schema/query.py daily.steps
python3 experiments/dbt_metrics/metric_schema/query.py daily.steps --profile daily/synthetic
```

The first command builds a fresh synthetic bundle, runs the ten parity groups,
queries every active ID, requires a typed sample from the declared model for each dbt column binding, and loads each fixture profile into
a new PostgreSQL schema with a restricted reader. The output at
`artifacts/unified-metrics.json` is replaced only after verification succeeds.
The bundle lists definitions, execution sources, dbt column bindings where
available, and individual values keyed at their native grain. Fixture profiles
belong to independent experiments. A time series must stay within one profile
and use comparable source versions and units.

The [table DDL](metric_schema/schema.sql) defines the catalog and fact tables. The [dbt reader](models/metric_read_v1.sql) and the standalone SQL loader share the projection for `metric_read_v1`. The model is opt-in with `profile_metric_reads`. Its two sources use the dbt target schema, and `profile_metric_read_roles` lists the roles that may read the view. Those roles also need SELECT on their underlying profile tables. The view exposes numeric, text and
boolean columns for ordinary SQL and the original JSON value for compound
types. It distinguishes an observed zero, an explicitly missing value, a
profile with no row, an input container, an unmapped key and a reserved target.
Read the original event clock and receipt clock separately. A missing
availability timestamp or source formula version stays unknown. The checkout
fingerprint identifies the local producer code used by the check and cannot
reconstruct what an earlier release knew. Sent reports retain their own archive.

To work in a local PostgreSQL scratch database, emit the loader for one fixture
profile and execute it there. The loader creates a new schema atomically and
fails if the requested schema already exists.

```sh
python3 experiments/dbt_metrics/metric_schema/profile_sql.py \
  --profile daily/synthetic --schema health_research_demo > /tmp/health-research-demo.sql
```

After loading, a dated value can be read with
`SELECT sample_key, numeric_value, source, value_status FROM health_research_demo.metric_read_v1 WHERE metric_id = 'daily.steps'`.
The same query shape applies to observations, aggregate periods, computed Go
results and recorded snapshots. The reusable Go function `ProjectFamilyValues`
projects a producer's DTO into catalog IDs, checking the producer package and
type. A PostgreSQL binding in the catalog names a queryable model and its
columns. A Go-backed value requires a captured result from the corresponding
reader. The synthetic bundle includes example rows rather than a continuous
history for any user.

For a populated local copy, `observations/persisted.py` reads catalogued
`metric_points` and 14 daily rollup columns from `daily_scores`, plus completed nights, workouts, Energy
snapshots and Readiness target snapshots in one profile schema. Daily rollups use the stored date and `computed_at`; the per-row formula version is unknown. Use the pilot
virtualenv, an explicit loopback DSN in `HEALTH_METRIC_READ_DSN`, and a
private output directory. Its JSON contains reader records for this subset.
Other derived values require their Go or dbt readers. For `daily_scores`, the exported availability clock is the row’s stored `computed_at`. Several writers can update one row, so its timestamp alone cannot establish when an individual field first became known. The export reflects the
currently stored rows. Use the report archive for an explanation as sent.

```sh
experiments/dbt_metrics/.venv/bin/python experiments/dbt_metrics/observations/persisted.py \
  --schema local_profile --profile local-copy --output ./private/persisted.json
python3 experiments/dbt_metrics/metric_schema/profile_sql.py \
  --persisted ./private/persisted.json --profile local-copy --schema local_research \
  > ./private/load-local-research.sql
```

### Preferred sleep source

Run `python3 experiments/dbt_metrics/scripts/test_preferred_sleep_source.py` from the repository root with Go, Docker and the pilot's Python environment installed. Set `DBT_BIN` to use an existing dbt executable. The runner creates a disposable PostgreSQL 16 database bound to loopback.

Three views compare wake-time source selection, sleep regularity and the saved packet decision against the storage readers. The synthetic captures cover exact source labels, missing or short captures, automatic ties and packet withdrawals. The packet view reads the stored decision at its recorded generation and revision. Older decisions can lack preference metadata.
