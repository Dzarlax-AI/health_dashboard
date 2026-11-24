# Local five-profile archive checks, 2026-11-24

One repeatable-read export per profile supplied the raw points, source hours and daily rows to a disposable PostgreSQL database accessible only through a private Unix socket. Profiles ran sequentially. Exports contained only the columns used by the comparison. Each temporary profile database was dropped after its check.

Generate the SQL with `python experiments/dbt_metrics/scripts/verify_exact_aggregate_archive.py`, then execute it with `psql -XAtq -v ON_ERROR_STOP=1` against the restored `source_data` schema. The script compares the branch's dbt hourly and daily macros with an independent implementation for active energy, HRV, respiratory rate and six sleep quantities.

| Profile slot | Calendar dates | Reference hourly cells | Hourly differences | Daily differences |
|---|---:|---:|---:|---:|
| 0 | 4,311 | 162,656 | 0 | 0 |
| 1 | 34 | 609 | 0 | 0 |
| 2 | 235 | 1,768 | 0 | 0 |
| 3 | 4,077 | 63,122 | 0 | 0 |
| 4 | 132 | 3,997 | 0 | 0 |
| Total | 8,789 | 232,152 | 0 | 0 |

The daily checks cover nine metric columns on each date, including missing values. This run covers the selected aggregate families. Historical Readiness, Today and Energy decision replay requires the effective input packet at decision time. All five existing production journals contained zero packets, so historical replay parity for these decisions remains unmeasured.

## Physical step hours and sleep history

A separate five-profile repeatable-read export included step observations, receipt content types, saved source hours, withdrawals and published daily steps. The independent Python reducer in `scripts/archive_step_oracle.py::archive_steps` rebuilt the daily series by source and physical hour. The published series had 3,448, 17, 221, 4,063 and 130 points respectively in slots 0–4. All 7,879 points matched. There were zero missing or extra dates and zero dates that required retaining a daily-only value. This comparison used the existing publication as the candidate. It did not rebuild the full worker graph.

The following PostgreSQL integration tests passed on an isolated synthetic schema. They cover the autumn clock change with UTC aliases, explicit withdrawals through import and live ingestion, and concurrent publication.

```sh
HEALTH_DB_TESTS=1 go test ./internal/storage -count=1 \
  -run '^Test(StepChartsCountPhysicalInstantsAcrossOffsetSpellings|PhysicalStepWithdrawalRepairsAcceptedMutations|AppleHealthXMLImportStepWithdrawalAndAdminRemoval|StepWithdrawalFencesConcurrentPublication)$'
```

On the retained November 18 archive, dbt built `semantic_sleep_history_v1` with `semantic_shadow` and `sleep_duration_reference` enabled. Executing `scripts/verify_archive_sleep_history.sql` checked 5,610 date pairs against independent eligibility/exclusion rules and found zero differences. This result covers one profile.

## Native catalog acceptance on five isolated copies

Each copy used an independent read-only, repeatable-read export. Each local database was migrated to schema contract v19 and installed with a tenant-owned schema and a restricted PostgreSQL role. The export included the selected metric points, source and derived tables, decision receipts and whitelisted configuration needed by the graph. Raw receipt content was limited to relevant HRV evidence. No notification process or AI provider was started.

Run the following from the repository root after restoring an isolated tenant into PostgreSQL. Set the normal libpq variables for the **local** tenant role and database. `TENANT_SCHEMA` is its schema. `DBT_PROFILES_DIR` points at the matching dbt PostgreSQL target. Use the environment with the pilot's pinned dbt dependencies.

```sh
pilot=experiments/dbt_metrics
vars=$(python "$pilot/scripts/native_catalog_vars.py" "$TENANT_SCHEMA")
dbt run --project-dir "$pilot" --profiles-dir "$DBT_PROFILES_DIR" \
    --select metric_read_v1 --vars "$vars"
python "$pilot/scripts/verify_native_catalog_archive.py" \
    --schema "$TENANT_SCHEMA" --timeout-seconds 600 --cache-night-reads > native-catalog-counts.json
```

On the largest copy, the verifier reused the actual installed view’s night rows across comparisons in a connection-local temporary table. PostgreSQL kept the source reads read-only.

The verifier reports per-ID status counts and checks source rows, sample keys and typed values against retained records. The generic observations also check source, timestamps and quality. The saved projections check metadata. Independent calendar, HRV, stage, workout and oxygen calculations check the published values by their own keys. Oxygen daily means allow an absolute error of `1e-12` for a different summation order. Independent interval-derived stage minutes allow one micro-minute absolute error, or a relative error of `1e-10`. The verifier reports missing reference rows, missing candidate rows, duplicate keys, value differences and timed-out cases.

A separate retrospective test selects the most recent local archive date at or before the test date with recorded sleep or HRV. It captures two Readiness decisions, Energy and Today by executing the storage paths, with the provider and notification services absent. The test gives the exact transaction's new inputs to the compiled worker SQL and compares its JSON result to each newly recorded result. Its transaction rolls back all capture and worker-key changes. This measures the reconstruction at replay time. Original report-time inputs would require original decision receipts.

```sh
dbt compile --project-dir "$pilot" --profiles-dir "$DBT_PROFILES_DIR" \
    --target-path "$LOCAL_TARGET_PATH" \
    --select hdbt_readiness_decisions worker_energy_decisions worker_today_decisions \
    --vars '{"hdbt_decision_worker":true}'
HEALTH_ARCHIVE_DECISION_SQL="$LOCAL_TARGET_PATH/compiled/health_dbt_metrics_pilot/models" \
    go test ./internal/storage -count=1 -run '^TestNativeArchiveRetrospectiveDecisionReplay$' -v
```

### Results on November 24, 2026

All five installations exposed the complete catalog of 456 IDs. Across the five copies, 251 distinct IDs had at least one available value. Every one of those IDs had an independent comparison on each profile where it had a value. Across 14,308,507 expected rows there were zero missing or extra keys, duplicate candidate keys, value differences outside the stated tolerances or query errors. This total counts a stored observation or snapshot once per tested metric field.

| Anonymous profile | Catalog IDs read | IDs with available values | IDs with only missing values | IDs with no rows | Non-value IDs | IDs with reference checks | Reference rows |
|---|---:|---:|---:|---:|---:|---:|---:|
| 0 | 456 | 249 | 13 | 191 | 3 | 289 | 8,605,279 |
| 1 | 456 | 155 | 7 | 291 | 3 | 289 | 29,657 |
| 2 | 456 | 139 | 4 | 310 | 3 | 289 | 48,809 |
| 3 | 456 | 239 | 15 | 199 | 3 | 289 | 5,503,642 |
| 4 | 456 | 157 | 21 | 275 | 3 | 289 | 121,120 |

Across the fleet, 13 IDs produced only missing values, 189 had no rows anywhere and 3 represented catalog dispositions without a value. These 205 IDs have no positive historical value to compare. The 289 IDs with reference checks per profile include empty expected result sets. For the four copies without any retained shadow-sleep candidates, the original source-table counts were zero and all three public IDs returned `no_row`. On profile 0, independently sweeping the boundaries of the winning candidates matched 120 durations, 120 conflict flags and 600 stage fields.

[Per-profile family counts](fleet_native_catalog_groups.csv) assign each ID to its first declared binding so totals are additive. For the 51 feature IDs and the wake timestamp that have additional prospective sources, retained-source candidate counts equalled all actual catalog rows for each ID. The archived copies had zero decision receipts in each tenant. Historical replay from original Readiness, Energy, Today, feature or wake decision-time inputs therefore remains unmeasured.

The retrospective test passed separately on all five copies. Out of 20 newly captured decisions, the compiled worker models reproduced 10 Readiness results, 5 Energy results and 5 Today results. All five transactions rolled back.

The schema-independent dbt model/macro and binding-source SHA-256 was `bd191b779156dc361d05390338323ddb79626b36f0291faf8a0052ef29098020`. The compiled view also matched across the three disposable database names after replacing the local database name with a constant. Its SHA-256 was `b5d1d954105840dd873c07c036715eedcf3f90e48eead3f5b4e4b175edb13031`.

After validating the aggregate receipt, the local PostgreSQL server was stopped. The five compressed exports, restored databases, scratch queries and private logs were deleted.

### Repeat after primary writer migration

The current shared primary formulas were checked on five freshly restored local copies. Report bodies and history rows were excluded from this export. The previous run above used a different input scope. Each profile exposed 456 IDs and ran independent comparisons for 289 IDs, including expected empty results.

| Anonymous profile | IDs with values | Compared rows | Differences or errors |
|---|---:|---:|---:|
| 0 | 241 | 8,605,388 | 0 |
| 1 | 147 | 29,397 | 0 |
| 2 | 139 | 48,809 | 0 |
| 3 | 231 | 5,503,502 | 0 |
| 4 | 157 | 121,210 | 0 |

The expected, candidate and unique row counts were equal for every comparison. Every available ID had an independent check on each profile where it had a value. The union had 243 available IDs and 915 populated profile/metric pairs across 14,308,306 compared rows. The model and macro digest began with `cd0ac33ab43f`. Historical decision input journals were empty on all five copies.

A separate rollback-only replay reproduced all 20 newly captured decisions across five archives, including ten Readiness, five Energy and five Today results. A one-observation correction in a seven-day window on each copy gave identical incremental and clean hourly and daily results across 5,358 hourly and 35 daily rows. The comparison used symmetric EXCEPT ALL across all columns. Sent-report immutability was checked on a synthetic archived report.
