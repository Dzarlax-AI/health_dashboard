# Verifying the shared dbt catalog

The local graph exposes 456 catalog identifiers through `ReadCatalogMetricPage`. Of these, 330 bind to named model fields, 123 preserve original observations, and three have explicit non-value dispositions. Each returned sample carries its native key, source, type and missing-value status. Reading a stored decision establishes what was recorded. Reproducing its calculation also requires the inputs available at the time.

## Reproducing the checks

Run these commands from the repository root after setting up the dbt environment described in the pilot README. Catalog assembly uses the companion Health Sync checkout to verify the iOS ingress definitions.

```sh
python3 experiments/dbt_metrics/scripts/run_profile_graph.py
python3 experiments/dbt_metrics/metric_schema/run.py --output /tmp/health-synthetic-catalog.json
PATH="$PWD/experiments/dbt_metrics/.venv/bin:$PATH" experiments/dbt_metrics/.venv/bin/python experiments/dbt_metrics/scripts/check_tenant_aggregate_worker.py
go test ./... -count=1
```

For an authorized archive, compare both readers on the same private snapshot of each profile. The CLI in `scripts/verify_catalog_archive.py` emits model selectors, graph variables and comparison SQL. The family-specific `verify_archive_*` scripts compare projections or rerun formulas at the stated grain. `scripts/catalog_archive_matrix.py` combines the results and distinguishes exact agreement, differences, empty comparisons and absent evidence. Keep profile-level reports local.

## Product scope

The tenant worker publishes hourly aggregates and all 14 modeled daily fields. The opt-in completed-day reader uses the tenant’s existing calendar and requires a ready publication at the current source generation and formula. The selected date retains its live calculation. The all-time steps chart has an independent switch. Contract v19 adds the tenant-native catalog installation and decision publication described below. The isolated profile graph remains the reproducible synthetic fixture.

## Archive evidence

All active definitions have a comparison on the five-profile archive matrix. Positive comparisons include native observations, stored values and independently reconstructed calculations. Ten definitions disagreed with previously materialized values and passed an exact comparison after deterministic aggregation and withdrawal repair. The matrix retains the old discrepancies next to the independent recalculation. Each of the 31 identifiers with empty or NULL archive results has a non-null example in the verified synthetic bundle.

The historical Readiness score and band are checked on reconstructible calendar dates. Final Today fields and normalized illness evidence use the final briefing capture. Retrospective replay uses the data available during replay. Delivered reports retain the body recorded at delivery.

## Producer inventory by catalog identifier

Run `python3 experiments/dbt_metrics/scripts/catalog_producer_inventory.py` to
emit the full TSV with catalog owner, model and columns, producer evidence and
worker path. The command checks that every ID has exactly one disposition, that
model files and referenced runtime consumers exist, and that archived projections
name their underlying tables. Add `--summary` for aggregate counts.

| Source of the catalog value | Identifiers |
| --- | ---: |
| Native observations retained at their original grain | 123 |
| Fields projected from persisted tables, including workout records and Go-written snapshots | 115 |
| Fields projected from a captured decision | 19 |
| Fields in models sharing at least one formula with a production Go reader or writer | 167 |
| Other graph model fields without an identified shared Go SQL call | 29 |
| Input container, dormant target, and unclassified key | 3 |
| **Total** | **456** |

The worker path is a separate dimension. Fourteen fields bind to the daily
worker output. Twenty-four identifiers bind to the Readiness, Energy or Today models
whose solver results have an exact decision-receipt publication path in this
checkout. Some of these identifiers are aliases, bands, or final presentation
fields. A solver receipt alone does not establish a final briefing or publish
each catalog field as a separate row. The Today receipt supplies decision mode, evidence domains and signal keys. The other 418 IDs have no worker path
identified by this inventory. Shared runtime SQL means that production code
invokes a model's formula and identifies the call site. Per-profile enrollment,
individual column parity, and production traffic require separate verification.

## Tenant-native catalog read

The opt-in native mode installs four tenant-owned security-invoker views in one dbt transaction. The common catalog serves metric-ID requests. Typed sleep-goal, Energy snapshot and workout projections share their column definitions with the catalog models and preserve indexed lookups on the original keys.
The per-ID inventory covers all 456 definitions. It includes 453 value-bearing identifiers and three status-only entries. Native models cover
native observations, stored-record projections, published daily aggregates,
retained decision inputs and the calculations that can be replayed from them.
The catalog registry and supporting models compile as ephemeral SQL in native
mode. No fixture or separate profile graph is needed. The Readiness solver
receipt supplies raw scores. Its history score supplies the history band. The
nine morning presentation fields require the effective inputs and final
headline/illness context retained in a version-three briefing receipt. A receipt
without those inputs supplies no final result. The dashboard sleep-quality and
briefing section models likewise require the fields that their formulas consume.

Published Energy, preliminary Readiness, and Today decision fields require a
matching versioned receipt, the decision-read switch and shadow enrollment. A
retained component whose measured value is absent yields a missing sample. An
unobserved decision yields no sample for that decision. An unavailable daily
publication yields no available daily row. Unsupported IDs return an empty page.
The ordinary Go reader requires the tenant-owned view and the profile setting
`dbt_native_catalog_reads_v1=true`.

In an isolated local tenant schema with the complete storage contract, generate
variables with `python3 experiments/dbt_metrics/scripts/native_catalog_vars.py SCHEMA`.
Use that JSON with `dbt run --select metric_read_v1 --vars "$vars"` and a dbt
profile whose target schema and login belong to the tenant. The command builds
the common view and all three typed product projections. The read switch can be disabled without
changing source data or installed views.

The integration test installs the model with dbt, reads it through
`ReadCatalogMetricPage`, and checks the supported ID count, gate, native point,
stored goal, fresh and dirty daily publication, source-local clocks, retained decision revisions, and schema isolation. Run it from
the repository root with a disposable local PostgreSQL DSN in `READINESS_TEST_DSN`,
`HEALTH_DB_TESTS=1` and an absolute dbt executable path in `HEALTH_TEST_DBT`:

```sh
go test ./internal/storage -run '^TestNativeCatalogDBTView$' -count=1 -v
```

## Feature writer replay

All 51 IDs bound to `archived_features_v1` also have a prospective calculation
source, `native_feature_replay_v1`. Enabling decision capture records the
effective input window atomically with each successful feature write. A retry
with identical inputs reuses its receipt. A correction creates another revision.
Each replay key includes the date, sub-score and receipt revision. The stored
snapshot and the immutable result observed at decision time are available as
`archived_features_v1` and `recorded_feature_decision_v1`, respectively. Older
snapshots without receipts retain their stored projection.

| Feature writer | Catalog fields | Inputs to replay |
| --- | ---: | --- |
| Recovery stability | 17 | Eligible sleep efficiency observations, the original seven daily stage summaries and the effective epoch start |
| Passive efficiency | 12 | Eligible walking heart-rate observations and the effective epoch start |
| Acute risk | 12 | Dated HRV and resting HR samples, including unpaired days, and the effective epoch start |
| Chronic load | 10 | Dated eligible Recovery rolling targets and the effective epoch start |

The replay applies the shared feature-window SQL to each input stream. Recovery
also classifies the seven stage summaries and calculates sleep debt and capture
confidence using the dated judgments. Acute risk counts days with both channels.
Chronic load counts eligible recovery days. Both apply the warmup rule to the
count in the current epoch. The integration test invokes all four product writers
and compares each of the 51 non-null, typed results with its saved projection.
It then changes a stored projection and checks the original receipt independently.
Upstream eligibility selection and source epochs are inputs to this replay.


Wake has two sources. `archived_wake_time_v1` reads the exact persisted wake timestamp. For a prospective decision, `wake_candidate_v1` selects from its immutable two-day sleep segment batch. `wake_confirmation_v1` uses the observed decision time, durable quiet clock, step count and prior typical wake minute. It exposes the candidate under the original wake metric with a separate revision key when its timestamp, source, signal, confidence, readiness and reason match the decision. The replay state describes that decision. A previously finalized stored day can retain final status during a new provisional evaluation. Capture is opt-in, skips an input batch larger than 4,096 segments and shares identical batches across confirmation states. Historical days lacking a confirmation receipt retain their stored timestamp. The scheduler continues to reserve and send reports through the existing delivery path.
