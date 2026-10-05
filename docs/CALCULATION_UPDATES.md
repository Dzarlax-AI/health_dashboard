# Updating Health calculations

Live processing remains Go-owned. Aggregation SQL is embedded in the binary;
the synthetic dbt project is an independent comparison tool. It is not a
production refresh job.

## Where to change a calculation

| Layer | Source | Responsibility |
| --- | --- | --- |
| Bucket and source policy | `internal/storage/aggregation_policy.go` | Known SUM metrics, daily mappings, sleep selection rules and aggregation contract identity |
| Hourly/daily SQL | `internal/storage/sql/aggregates/` | Embedded live and legacy queries; execution and transactions stay in `aggregates.go` |
| Pure business formulas | `internal/health/` | Readiness, activity, Energy and sleep calculations without database I/O |
| Versioned derived writers | `internal/storage/derived_metrics.go`, `wake_detection.go`, readiness writers | Input hashes, formula/feature versions, persistence and eligibility |
| Independent comparison | `experiments/dbt_metrics/` | Synthetic expectations, Go/dbt comparisons and local timings |

The exported `SumMetrics` map remains compatible with existing callers. Treat
it as read-only. Add or change canonical definitions in the policy catalog;
do not introduce another list in a transport package.

## Existing version storage

| Output | Existing identity | Meaning |
| --- | --- | --- |
| `derived_metrics` (including wake time) | `formula_version`, `inputs_hash`, calculation/finalization timestamps | Formula and exact input identity are persisted per metric/date |
| Readiness target and naive baseline snapshots | `formula_version`, `source_epoch` | Persisted writer/formula identity within the source epoch |
| Readiness feature snapshots | `feature_version`, `source_epoch` | One canonical payload per date/sub-score, overwritten by upsert |
| Energy snapshots | `formula_version`, components audit trail | Existing Energy formula identity; historical EOD backfill is separate from live buckets |
| Readiness daily score | `score_version` | Existing score revision |
| Raw hourly/daily rollups | Code-level aggregation contract version and checksum | Identifies the implementation in comparison reports; it does **not** stamp historical cache rows |

Do not interpret an updated code checksum as proof that every historical row
has been recalculated. Persisting rollup versions or running a production
migration requires separate design and authorization.

## Preserve context-specific behavior

Live daily AVG gives each source mean equal weight. Legacy daily AVG gives
each persisted hourly row equal weight. Unequal hourly coverage can therefore
produce different values. An unknown ordinary metric defaults to AVG; an
unknown `sleep_` metric follows SUM in the live sleep writer but the existing
catalog/default in legacy hourly aggregation.

Sleep selection has a conservative divergence check (minimum above one hour,
maximum greater than minimum × 1.4), then Watch/RingConn/maximum priority.
Midnight summaries take precedence over fragments from the same source/day.
Incomplete picked sleep preserves the previous daily block. A complete
coarse replacement can clear prior stage values. Ordinary nullable daily
fields use COALESCE. Non-sleep hourly upserts can retain an old row when all
points become invalid; a clean rebuild has no previous row to retain.

These are explicit existing contracts. Correcting or unifying them is a
formula/behavior change, not a routine SQL cleanup.

## Workflow

1. Identify the affected writer and intended date/source policy. State whether
   the change should preserve, replace, or clear prior values when inputs are
   missing or invalid. Include timezone/day-key and source-epoch behavior.
2. For refactoring, record a baseline **before editing** with the guarded
   synthetic runner below. Keep that baseline for comparison; never overwrite
   it with the candidate output.
3. Edit the policy, SQL or pure formula. For a semantic change, bump the
   relevant existing formula/feature/score version and the aggregation
   contract version if rollup semantics change. Update independent expectations
   and dbt models only when the behavior change is intentional.
4. Test edge cases: competing sources, unequal coverage, source ties, midnight
   summaries, incomplete sleep, late data, quality invalidation, repeat writes,
   untouched dates, and incremental versus clean rebuild.
5. Compare old/new results by metric/date/source. Explain every difference
   with an input example and intended rule. Synthetic parity is a prerequisite,
   not proof of clinical validity or production performance.
6. For a future real-data comparison, define a read-only/isolated comparison
   scope first. Choose affected dates and required lookbacks, inspect coverage,
   review the diff, then authorize the specific production recalculation and
   release. Whole-cache force rebuild is not an automatic consequence of a
   formula edit.

## Local synthetic checks

Use Python 3.12+ and local Docker. Run one driver at a time; generated artifacts
and dbt profiles are shared within the experiment directory.

```bash
python3 experiments/dbt_metrics/scripts/run_go_contract.py --record
# Make the refactor, keeping the recorded baseline intact.
python3 experiments/dbt_metrics/scripts/run_go_contract.py --compare
python3 experiments/dbt_metrics/scripts/run_pilot.py
go test ./internal/storage ./internal/health
go test ./...
make contract-check
CGO_ENABLED=0 go build -o /tmp/health-calculation-modularity-server ./cmd/server
```

`--record` refuses to overwrite an existing ignored baseline. Each driver
creates its own marker-checked, loopback-only synthetic PostgreSQL container
and removes only that owned container and volume. The Go harness never reads
`DATABASE_URL`. Legacy coverage deliberately calls the aggregate writers
directly; it does not invoke later baseline/sustained-HR backfill stages.

The main pilot covers the existing completeness/coarse/quality differences
and benchmarks. The contract runner also pins unequal source coverage and
unknown metrics and compares live, legacy-force and legacy-incremental
snapshots against the recorded implementation. Ordinary DB integration tests
remain opt-in; a skipped integration test is not a successful DB check.

## Startup recovery and historical corrections

Startup uses the tenant-local `_cache_maintenance_v1` journal in `settings`.
Its identity includes aggregate SQL/policy, readiness score, auxiliary cache,
Energy, and all readiness writer formula/feature versions. Bump the relevant
version when semantics change. The first unknown identity requires a historical
baseline; an exact completed identity with no dirty dates skips historical work.
A matching interrupted target resumes its persisted phase/date cursor.

Recovery runs bounded date transactions in dependency order: aggregate, overnight
baseline, sustained HR, Recovery, Passive Efficiency, Acute Risk, Chronic Load,
and readiness/Energy. The cursor advances only after commit. Background units
have a 30-second SQL deadline and yield to foreground cache waiters between
units. Continuous foreground work may delay maintenance. Source changes and
per-date dirty generations commit together; completion cannot clear a newer
correction. Final dashboard publication and the completion marker commit together.

Historical corrections rebuild changed live aggregate dates, then auxiliary and
writer-major dependencies from 17 days before the earliest changed date through
today. The extra horizon covers Chronic's 14 forward days and Recovery's three
forward nights. Synthetic `backfilled` Energy EOD rows are recalculated; observed
intraday rows remain the record of what was calculated at that time. Historical
hourly Energy graphs are not reconstructed by this repair.

The startup worker retries incomplete tenants without monopolizing the installation
queue. Manual backfill commands remain available. Local parity and isolated DB
checks do not prove production latency; release acceptance requires a real sync
and repeated startup on the deployed image pair.
