# Readiness Adaptive Monitoring Design

Date: 2026-05-26

## Summary

The naive-layer readiness monitoring should avoid noisy warnings caused by expected forward-label lag, while still surfacing real tenant data gaps. The current fixed mature-window change handles intrinsic label lag for acute and chronic labels, but it does not adapt to tenants whose upstream data arrives late or in batches.

This design keeps target math deterministic and adds a tenant-aware freshness guard. Monitoring must never search for a convenient green window. It should choose the latest defensible evaluation window from target contract lag plus observed input freshness, then separately warn when a tenant's inputs are stale.

## Goals

- Suppress expected `event_window_data_missing` warnings at the fresh edge of forward-looking targets.
- Adapt monitoring windows to tenant-specific input latency.
- Keep real coverage problems visible, such as Maria's `chronic_label` failure caused by sparse Recovery rolling rows in a mature window.
- Make the reported window explainable in the UI and API.
- Keep the implementation read-only.

## Non-Goals

- Do not change target writers, eligibility semantics, target rows, feature rows, or baselines.
- Do not recompute or mutate historical readiness data.
- Do not hide coverage warnings by scanning backward until an OK window is found.
- Do not add per-tenant settings in this pass.

## Current Behavior

`LoadReadinessMonitoringSummary(asOfDate)` computes coverage over a 14-day window ending on `asOfDate`. That is appropriate for same-day daily and rolling targets, but noisy for forward-looking labels:

- Acute Risk labels need `t+1..t+3`.
- Chronic Load labels need `t+1..t+14`.

The first fix moves coverage windows by the target contract lag: 3 days for Acute Risk and 14 days for Chronic Load. Live checks showed this removes expected chronic warnings for `health`, and removes Maria's `chronic_acute_density` warning. Maria's `chronic_label` remains warning because the mature window still has real Recovery rolling gaps.

## Desired Behavior

For each tenant and target, monitoring computes:

```text
contract_end = as_of_date - target_contract_lag_days
input_stable_end = latest date where the target's upstream inputs are stable enough to evaluate
coverage_end = min(contract_end, input_stable_end)
coverage_start = coverage_end - 13 days
```

If `input_stable_end` is older than `contract_end` by more than an allowed grace period, monitoring emits a separate stale-input warning. Coverage still runs against `coverage_end`, so the operator can distinguish:

- "the label window is not mature yet" from
- "the user's inputs are late/stale" from
- "the mature window has real missing or ineligible rows."

## Target Contracts

Target contract lag is deterministic:

| Sub-score | Target kind | Contract lag |
| --- | --- | --- |
| recovery_stability | daily_point | 0 days |
| recovery_stability | rolling_3d | 0 days |
| passive_efficiency | daily_point | 0 days |
| passive_efficiency | rolling_3d | 0 days |
| acute_risk | event_t1_t3 | 3 days |
| acute_risk | event_strict_t1_t3 | 3 days |
| chronic_load | chronic_label | 14 days |
| chronic_load | chronic_acute_density | 14 days |

These values come from target definitions, not from observed data.

## Input Stability

Input stability is computed per target from rows the target depends on:

- Recovery Stability and Passive Efficiency targets: use their own `target_snapshots` rows because they are direct targets and already encode data eligibility.
- Acute Risk labels: use Acute Risk target rows for the candidate label window. Missing or ineligible rows mean the stable date cannot move past that point.
- Chronic `chronic_label`: use upstream `recovery_stability/rolling_3d` rows because chronic deterioration depends on Recovery rolling values.
- Chronic `chronic_acute_density`: use upstream `acute_risk/event_t1_t3` rows because acute density depends on Acute OR labels.

The stability function should be conservative: a date is stable only when the required upstream rows exist and are eligible enough that the writer could honestly evaluate the label. It may reuse existing target rows as the source of truth rather than reimplementing writer internals.

## Stale Input Warning

Add a monitoring row or alert category for stale inputs:

```text
input_staleness_days = contract_end - input_stable_end
```

Default thresholds:

- `ok`: 0-2 days stale beyond contract lag.
- `warn`: 3-7 days stale beyond contract lag.
- `critical`: more than 7 days stale beyond contract lag, or no stable date found in the last 30 days.

These thresholds are monitoring-only and can be constants in the first implementation. They do not affect target eligibility or user-facing readiness scores.

## API Shape

Extend `ReadinessCoverageRow` with:

- `WindowFrom`
- `WindowTo`
- `ContractLagDays`
- `InputStableTo`
- `InputStalenessDays`
- `InputStalenessStatus`
- `InputStalenessReason`

Existing fields remain compatible:

- `Rows`
- `ExpectedRows`
- `MissingRows`
- `Eligible`
- `EligiblePct`
- `Status`
- `ReasonCounts`

Overall status should consider both coverage status and input-staleness status.

## UI Behavior

The monitoring table should show the evaluated window in the value or comparison column, for example:

```text
12/14 eligible, 14/14 rows (86%) · window 2026-04-29..2026-05-12
```

If input freshness is stale, show it as a separate note:

```text
inputs stale by 5d · last stable 2026-05-07
```

This prevents operators from misreading a shifted mature window as "today is fine."

## Data Flow

1. Handler resolves tenant scope as today.
2. Storage loads monitoring summary for that tenant.
3. For each target, storage resolves target contract and input stable date.
4. Storage computes coverage over the chosen window.
5. Storage computes stale-input status.
6. UI renders coverage plus freshness context.

## Test Plan

- Unit test: chronic fresh-edge `event_window_data_missing` rows do not produce coverage warning when mature upstream window is healthy.
- Unit test: mature upstream gaps still produce coverage warning.
- Unit test: if upstream data is stale beyond threshold, coverage may be OK but stale-input status is warn.
- Unit test: no stable date in last 30 days yields critical stale-input status.
- UI test: fragment renders the evaluated window and stale-input note.
- Existing tests: `go test ./internal/storage -run ReadinessMonitoring`, `go test ./internal/ui -run ReadinessRedesignMonitoring`, `go test ./...`.

## Rollout

Ship behind the existing admin monitoring surface; no migration is required. The change is read-only and can be rolled back by reverting the monitoring storage/UI commit.

## Open Questions

- Should stale-input thresholds be constants for now, or should they be surfaced as admin settings later?
- Should stale-input status appear as separate rows, or be folded into each coverage row?

Recommendation: keep constants for the first implementation and fold stale-input status into each coverage row, with overall status considering both coverage and staleness.
