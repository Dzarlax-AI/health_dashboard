# Morning sleep report and readiness I/O acceptance — 2026-09-30

Implemented locally on `codex/sleep-report-readiness`, based on `76ea15c9363933f5091098dc520da4fefe90efea`. No commit, PR, release, production mutation, external AI-provider request, or Telegram send was performed for this change.

## Measured readiness result

SQL calls decreased by 17.4–17.6%, with identical target, feature, naive-baseline, and monitoring-serving outputs. Repeated elapsed times move in both directions: these measurements do **not** establish a reliable runtime improvement or a production latency benefit. The exact write counts remain unchanged. No batching was added because implicit batch transactions would change partial-write behavior; the accepted optimization removes repeated source-epoch reads only.

Baseline source was archived from `76ea15c`; the same final opt-in harness ran against both versions. A disposable local PostgreSQL 16 database used synthetic 365-day wearable history. The harness uses the actual routine window function: 1/7/30 affected days expand to 15/21/44 calculated dates. Four writers run sequentially. `pg_stat_statements` was reset between samples, with no concurrent database workloads. Three matched runs are retained, without discarding slower samples. First-write means empty derived snapshot tables; repeat-upsert retains their rows. Both run against warm PostgreSQL shared buffers; these are not cold-cache I/O tests.

| Affected days | Calculated days | Output state | Baseline samples (ms) | Optimized samples (ms) | Median (ms) | SQL calls | Epoch reads |
| --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | 15 | first-write | 194.3, 209.0, 242.5 | 228.0, 215.8, 167.9 | 209.0 → 215.8 | 686 → 567 | 120 → 1 |
| 1 | 15 | repeat-upsert | 186.7, 207.1, 220.9 | 207.3, 215.8, 235.7 | 207.1 → 215.8 | 686 → 567 | 120 → 1 |
| 7 | 21 | first-write | 320.2, 262.1, 283.9 | 251.3, 261.8, 294.5 | 283.9 → 261.8 | 956 → 789 | 168 → 1 |
| 7 | 21 | repeat-upsert | 338.9, 232.5, 278.1 | 246.9, 231.0, 288.0 | 278.1 → 246.9 | 956 → 789 | 168 → 1 |
| 30 | 44 | first-write | 577.1, 716.9, 630.1 | 638.7, 443.3, 678.9 | 630.1 → 638.7 | 1991 → 1640 | 352 → 1 |
| 30 | 44 | repeat-upsert | 555.0, 613.9, 613.9 | 573.8, 507.0, 613.7 | 613.9 → 573.8 | 1991 → 1640 | 352 → 1 |

The table/serving hashes match for every run and window. Snapshot hashing includes every logical target/feature/baseline field and omits only `computed_at`. Serving parity covers `LoadReadinessMonitoringSummary`; the wider Today API was not benchmarked or replayed here. Existing readiness/Energy formulas and the four-writer dependency order are unchanged.

### Retained output digests

| Affected days | Snapshot MD5 | Serving SHA-256 |
| --- | --- | --- |
| 1 | `951fa18d8b08e20ff1288e74356b85ff` | `1da42cb1afec09da60c3cb0b5616b91f48794d5042bbce384bc3482fe740aa6f` |
| 7 | `8fd4ab8cfd4572d7ff61b20e24e5ddae` | `8320a1af250c0094f8b797fe30f47a903885d28a489bc340119b3d0b8f83b369` |
| 30 | `110fce03742084a709b06af9837ba22c` | `d4f34e4b7ba1745541c92ec0866e49b305fcd01e690e5b2186e81d3fd4a77a47` |

## Telegram result

Both plain and rich reports start with the dated sleep record, comparable prior-night baseline when available, and a sleep explanation. The source is the canonical completed-night record; a strictly newer legacy observation is shown with unknown completeness and cannot borrow canonical metadata. Current incomplete canonical records retain priority over same-date aggregates. Baselines require at least seven finalized comparable nights in the previous 30 days, with the same source, epoch, and algorithm.

The report and generator share exact evidence and generation fingerprint calculation. Only a complete matching five-block bundle can supply SLEEP; wrong date/language, updated evidence, disabled AI, or changed model/reasoning/prompt/output limit cannot admit stale text. A current complete plausible provisional night may use AI, with a provisional label. Older, missing, partial, unknown, or outlier data uses localized deterministic limitations. Stages and awakenings are omitted because this canonical packet does not establish their coverage. General-data stale warnings remain based on briefing date. Generic sleep reasons are excluded below the primary canonical sleep section to avoid contradictory duplicate durations.

Scheduling, wake/check-in gates, at-most-once reservations, public API/schema, B1 flags, database pool sizes, and writer parallelism are unchanged. This fixes sleep selection and presentation; it does not prove or change delivery timing. No real morning message or live provider response was tested.

## Verification

- `GOCACHE=/tmp/health-sync-audit-go-cache go test ./...` — passed; DB opt-in tests skip in this general run.
- `HEALTH_DB_TESTS=1 READINESS_TEST_DSN=<disposable-loopback-db> GOCACHE=/tmp/health-sync-audit-go-cache go test ./internal/storage -run 'Test(Morning|SourceEpochRunCache|ReadinessBackfillInvalidRange|Backfill|RecoveryStability|PassiveEfficiency|AcuteRisk|ChronicLoad|ResolveSourceEpoch)' -count=1 -v` — 49 top-level tests passed, including epoch error/recovery, tenant isolation, all four writers, and morning cache/selection cases.
- `GOCACHE=/tmp/health-sync-audit-go-cache go test -race ./internal/health ./internal/notify ./internal/storage ./cmd/server` — passed (DB opt-in tests disabled in this race run).
- `GOCACHE=/tmp/health-sync-audit-go-cache make contract-check` — passed.
- `git diff --check` — passed.
- Synthetic actual-formatter preview checked in EN/RU/SR: fresh AI, deterministic fallback, older night, and incomplete observation; no messages sent.
- Independent GPT-6.1 Sol/high review: no unresolved actionable findings. It identified and verified corrections to epoch fallback/sentinel handling, canonical facts on raw-data failure, and report caveats.

## Reproduce performance measurements

Use a disposable PostgreSQL instance with `pg_stat_statements` preloaded. Never point this harness at a shared or production database: it resets statement statistics and the test helpers create/reset isolated test schemas. Run the identical harness against the baseline and proposed source:

```sh
HEALTH_DB_TESTS=1 READINESS_IO_BENCH=1 READINESS_TEST_DSN=<disposable-loopback-db> GOCACHE=/tmp/health-sync-audit-go-cache go test ./internal/storage -run '^TestReadinessIOPerfHarness$' -count=1 -v
```

Raw evidence files generated for this acceptance: `/tmp/readiness-io-baseline-20260930.log`, `/tmp/readiness-io-optimized-20260930.log`, `/tmp/readiness-io-baseline-repeat-20260930.log`, `/tmp/readiness-io-optimized-repeat-20260930.log`. The repeat files contain two additional runs each. Final test logs use `/tmp/sleep-readiness-{go-test,db,race,contract}-final.log`.

## Execution and release status

GPT-6 Luna/high implemented renderer/tests and readiness I/O as separate agents; the lead owned shared evidence/hash contracts, integration, and final verification; GPT-6.1 Sol/high independently reviewed the actual diff and benchmark method. No provider model was changed; the morning prompt revision changes to `health-briefing-v4-sleep-night` so previous evidence cannot reuse its cache entry.

The user explicitly authorized deployment through a PR after local acceptance. PR creation, CI/review, and the mandatory tenant-schema deployment gate are now approved; this document records the pre-release measurements. Production performance remains unmeasured. Rollback is a source revert only, with no schema changes or data cleanup.
