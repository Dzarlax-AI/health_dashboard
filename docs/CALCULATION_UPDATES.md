# Updating Health calculations

Go schedules live processing and owns its transactions. The local migration embeds shared dbt SQL in selected Go readers and writers. The disposable dbt project runs those definitions against fixtures. See the [migration plan](ai-plans/2026-11-22-dbt-all-derived-metrics.html) for the verified scope.

## Aggregate contract 7

Positive, accepted observations are ordered by their original timestamp before an hourly
average. Summed metrics first choose the maximum in each writer minute, then add those
float32 minute values in chronological order. Daily means and sums process hours in
chronological order within each source. A daily average gives each source equal weight
and visits sources in PostgreSQL's C collation. These orders fix the rounding seen by
both historical and incremental writers. Step-specific alias and withdrawal handling
continues to use its dedicated projections.

If a represented source/hour contains only rejected observations, both average writers
remove its obsolete cached hour atomically. An hour absent from the incoming source set
does not prove withdrawal. If a daily input is missing, the existing per-field and
whole-night retention rules still apply. A completed full repair is a separate result
from a historical production cache that predates this contract.

The archive harness in `experiments/dbt_metrics/scripts/verify_exact_aggregate_archive.py`
checks raw projections and records previous cache differences. Its average-hour repair
mode also executes the application writer on a private copy. The verification matrix records the
independent recalculation separately from the prior publication.

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

Aggregate contract 6 counts equivalent timestamp representations of a usable step measurement once. The source, measurement grain, quantity and UTC instant must all agree. An observed nonzero offset takes precedence over a UTC rendering. Remaining ties use bytewise timestamp order, which gives the same local date on every replay. A repeated wall-clock hour with two distinct UTC instants contributes twice. Unknown offsets retain the legacy wall-hour rules.

Aggregate contract 5 introduced a rule that selects the step source independently for each physical hour, using the local hour and UTC offset.
Apple Watch (including Ultra) takes priority over iPhone, followed by other
devices. Within a priority tier the largest usable total wins. The daily value
sums these hours. The combined v2 hourly and daily graphs and latest value
use this ranking. Device-specific graphs expose each device's observations.
A withdrawal reselects the affected hour from its surviving sources. The final withdrawal
leaves a missing daily value. An incomplete incoming packet preserves valid
measurements previously received from other sources.

Live and historical day totals reconstruct physical hours from retained points
and their sync or import receipts. A verified hourly zero contributes a
measured zero. Two occurrences of the same clock hour can select different
devices. Their contributions share one label on the hourly chart.

Legacy hourly keys contain local time and source. If a cache cell outlives
its source observations and a later partial sync cannot reconstruct it, keep
the previous winner for the entire wall-clock hour. A confirmed restoration
or withdrawal can change that result. Device charts still expose the individual
sources. The bounded post-wake calculation uses timed observations because
an old whole-hour total cannot be allocated to minutes after waking.

The daily steps chart and Activity read published daily quantities from
`daily_scores` when the maintenance journal certifies the calculation. A
dirty date is reconstructed from physical hours in the same database snapshot.
An incomplete rebuild or missing journal requires reconstructing the requested
range. The detailed metrics API still computes observation extrema and source
breakdowns separately.

Aggregate contract 3 gives each source equal weight for daily HRV, resting
heart rate, oxygen saturation, VO₂ max and respiratory rate. Each source
contributes its mean across valid hourly rows to foreground, full, incremental,
and date-bounded writers and the unfiltered daily chart. Raw morning evidence
and per-source or intraday readings retain their own grain. An unknown ordinary
metric defaults to AVG. The live writer sums an unknown `sleep_` metric.
Historical hourly aggregation applies its catalog and default rule.

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

When a contributing HRV point is withdrawn, the quality sweep, authoritative
XML promotion or Auto Export removal records an exact-date repair in the
tenant's settings, in the same transaction as the point change. That transaction
also clears the date's HRV hourly/minute rows, daily HRV, readiness and score
version, and refreshes the dashboard snapshot. All cached readers therefore
withhold withdrawn values even if the subsequent repair fails. The morning
reader additionally suppresses pending cached HRV while allowing fresh samples.
The aggregate writer rebuilds surviving samples with its usual weighting rule
and clears the marker in one transaction. Other
dates keep their same-day fallback. Startup can recover a surviving marker
even if an older binary has already drained the general dirty journal. This
event-driven repair covers new confirmed changes; old caches without such an
event require separate evidence before any retrospective repair. When the
withdrawal removes the tenant's last hourly observation, the coordinator
publishes an empty dashboard and completes the matching dirty generations.
Archived delivery attempts retain their original bodies. The dbt pilot carries
the date's pending-repair flag in its HRV cache extract so its morning value
matches the Go reader throughout the repair.

The startup worker retries incomplete tenants without monopolizing the installation
queue. Manual backfill commands remain available. Local parity and isolated DB
checks do not prove production latency; release acceptance requires a real sync
and repeated startup on the deployed image pair.

## Revision-bound sleep projection pilot

Each tenant can pin one revision-bound pilot range and source set. A rollback
returns reads to Go and retains this association to enforce confirmed sleep
withdrawals against legacy records and stale replays. Subsequent preparations
use the same range and sources. Changing them requires an explicit handoff
that preserves the withdrawn dates already owned by the projection. Deleting
the publication row would discard that protection.

Candidate preparation and publication are separate operator actions. Review
the range and source coverage, retain unknown dates, and use a disposable
database for rehearsal. Synthetic parity tests cover calculation and state
transitions. Source selection on a physical device requires separate review.

## Daytime sleep in the EnergyBank experiment

`HEALTH_SLEEP_EARLY_OBSERVATIONS_ENABLED=1` enables early observations and
collects episode evidence for the local comparison as new projections are
prepared. The server default is off. The normal EnergyBank and Readiness
calculations continue to use their existing formulas. Early observations are
excluded from the revision-bound publication audit.

Use `cmd/sleep_shadow --compare-nap-energy-at RFC3339` with the restricted
tenant and schema options and a disposable database on a local Unix socket.
`--nap-energy-html` renders the Russian explanation. The comparison reads the
current database snapshot and reports immediate and gradual hypothetical
credits within the local date. It does not replay historical bank versions or
persist any credit. Dates prepared before the flag was enabled may have no
episode evidence. Calibration and real-device validation are required before
a formula is enabled for users.
