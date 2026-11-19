sleep_total_per_source AS (
    SELECT source, sum_val FROM per_source WHERE metric_name = 'sleep_total'
),
-- Pick ONE source for tonight from sleep_total totals; all five sleep_*
-- stages will be filtered to this source so phase ratios stay physically
-- consistent. Helper keeps thresholds in lockstep with the value-twin.
sleep_picked AS (
    SELECT {{SLEEP_PICK_SOURCE}} AS src
),
-- Atomicity gate: prevent mixed-source writes when MULTIPLE sources
-- contribute sleep_total for the night. With a single source there is
-- no mixing risk, and a strict 5-stage requirement would erase a real
-- night just because one stage happened to be 0 (e.g. a HAE-fed Apple
-- Watch night with sleep_awake = 0 — buildHourlyMetric filters qty>0
-- so the awake row never reaches hourly_metrics, completeness fails,
-- and all five sleep_* columns get NULL instead of the four real ones).
-- Therefore: when n_sources <= 1, trust the only source as-is.
-- When n_sources > 1, require ALL five stages from picked source so we
-- don't COALESCE-preserve a prior row's stage from a different device.
sleep_picked_complete AS (
    -- Conditional gate (option B per SLEEP_UNSPECIFIED_ROLLOUT.md):
    --   single source     → trust as-is
    --   multi-source + picked has all 5 traditional stages → complete (stage-tracking device)
    --   multi-source + picked has sleep_total + sleep_unspecified → complete (coarse-only device)
    -- Without the second clause, multi-source nights where MIN-pick lands
    -- on a RingConn-only source (2 metrics) would fall through to NULL
    -- writes and the prior block would survive untouched.
    --
    -- KNOWN LIMITATION (Issue #77): a multi-source night where the picked
    -- source emits ONLY sleep_total (no stages, no sleep_unspecified) does
    -- not match either branch — the gate fails, the prior daily_scores row
    -- is preserved, and that source's contribution is silently dropped for
    -- this night. Deliberate choice rather than oversight: accepting a
    -- single-metric pick would let a malformed third-party importer wipe
    -- a real staged night by writing only sleep_total. The v2.3 iOS
    -- client always pairs sleep_total with sleep_unspecified for coarse
    -- sources, and the HK XML importer in internal/applehealth/parse.go
    -- maps both AsleepUnspecified and bare Asleep to sleep_unspecified,
    -- so natively-imported data cannot hit this corner.
    --
    -- Regression coverage: internal/storage/sleep_gate.go mirrors this
    -- logic as a pure Go function (EvaluateSleepPickedComplete) so unit
    -- tests can exercise all four scenarios without a live Postgres.
    -- When changing the gate here, mirror the change in sleep_gate.go
    -- (and vice versa) — see sleep_gate_test.go for the expectations.
    SELECT (
        (SELECT COUNT(DISTINCT source) FROM sleep_total_per_source) <= 1
        OR (
          SELECT COUNT(DISTINCT metric_name) FROM per_source
           WHERE source = (SELECT src FROM sleep_picked)
             AND metric_name IN ({{SLEEP_TRADITIONAL_METRICS}})
        ) = 5
        OR (
          SELECT COUNT(DISTINCT metric_name) FROM per_source
           WHERE source = (SELECT src FROM sleep_picked)
             AND metric_name IN ({{SLEEP_COARSE_METRICS}})
        ) = 2
    ) AS ok
)
