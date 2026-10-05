# dbt metrics pilot results

**Outcome:** the dbt full reconstruction matched the existing Go hourly and daily writers on the accepted synthetic fixture and the scaled history. The incremental update path also matched across repeat refresh, late insertion into an old date, changed input value, staged-to-coarse sleep, and incomplete sleep after a complete result. The pilot remains experimental. The lead independently reran the complete driver on 2026-10-04 after reviewing the actual SQL and adding a guard against partial full refresh.

The run used Go 1.26.4 on darwin/arm64, Python 3.12.14, dbt-core 1.12.5, dbt-postgres 1.11.0, and PostgreSQL 17.11 on aarch64 Linux. PostgreSQL ran from the pinned image digest recorded in the runner. The baseline had 115 metric points, 101 hourly rows, and 15 daily rows. All 11 initial dbt build nodes passed, including nine data tests. Every human-authored expected value and absent-row assertion passed. The dbt role could read the synthetic source and a write attempt was rejected.

Incremental acceptance confirmed that repeating the same day does not create duplicate hourly keys; dates outside the selected set remained unchanged. An incomplete multi-source sleep update preserved the earlier complete 7-hour sleep block. A clean full reconstruction produced NULL for that date because no complete source existed in the raw state, demonstrating that this Go behavior depends on update history. A complete coarse-only update replaced staged sleep and cleared all four stage columns.

Quality invalidation exposed one intentional difference: Go retained a stale Apple Watch hourly step row of 35, so its daily value remained 35. The dbt model deletes and rebuilds the full selected day, removing that row and selecting the iPhone value of 100. A clean Go rebuild and dbt full refresh agreed at 100. This difference is documented in the experiment and is not a proposed product change.

The scaled workload contained 17,642 metric points across 380 dates, producing 17,627 hourly rows and 380 daily rows. Three full runs and three old-day updates were measured. Timings include each Go/dbt process startup; the Go harness wall time also includes exporting all results, while the dbt wall time ends when the model run exits. Go separately reports only the existing aggregate-writer time. Data tests are outside the benchmark timers. The local results were:

| Workload | Go elapsed (median) | Go aggregate writers (median) | dbt elapsed (median) |
| --- | ---: | ---: | ---: |
| Full reconstruction | 1.809 s | 1.207 s | 2.584 s |
| One old day | 0.605 s | 0.0106 s | 1.385 s |

The database and operating-system caches were warm or uncontrolled. These figures do not establish production performance or speedup. Separate full-history and one-day `EXPLAIN (ANALYZE, BUFFERS)` plans for both models are saved in ignored artifacts. The plans cover compiled SELECT statements and exclude dbt DDL and hooks.

The runner rejected missing affected dates, a non-list value, an invalid calendar date, a valid date absent from the fixture calendar, and a partial date list combined with full refresh. Output rows remained unchanged after the rejected runs. The lead independently confirmed that the exact marked container and its anonymous volume were removed after the run. The storage package tests and the pinned Python dependency check passed. Raw timings, identity marker, complete row sets, and plan text remain in the ignored `artifacts/` directory for local inspection.

The compiled SELECT plans measured 66.381 ms (full hourly), 1185.720 ms (full daily, including PostgreSQL JIT), 0.210 ms (one-day hourly), and 2.677 ms (one-day daily). These are individual warm/uncontrolled-cache observations, not three-trial SQL medians, and omit DDL, deletion hooks, merges, and client overhead.

**Recommendation:** keep this as an offline audit and historical-recalculation prototype. It provides explicit SQL models, dependency tracking, and repeatable data tests. The small daily-update workload pays substantial process/framework overhead, so the tested live aggregation path should remain in Go. This pilot does not establish performance on production-sized data or a tenant-safe publishing strategy. Its deliberate stale-row difference and history-dependent daily preservation must be resolved before any product migration.

The PostgreSQL adapter configuration and transactional hook behavior were checked against [official adapter documentation](https://docs.getdbt.com/docs/local/connect-data-platform/postgres-setup) and [official hook documentation](https://docs.getdbt.com/reference/resource-configs/pre-hook-post-hook). The results above come from the local driver, not from those documentation pages.
