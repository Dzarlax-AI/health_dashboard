WITH per_source AS (
    SELECT metric_name, source,
           AVG(avg_val) AS avg_val,
           SUM(avg_val) AS sum_val
    FROM hourly_metrics
    WHERE SUBSTRING(hour,1,10) = $1
    GROUP BY metric_name, source
),
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
),
agg AS (
    SELECT
      metric_name,
      AVG(avg_val) AS avg_across_sources,
      -- preferred SUM source (non-sleep): Apple Watch > iPhone > MAX(any)
      COALESCE(
        MAX(sum_val) FILTER (WHERE {{SUM_WATCH_CONDITION}}),
        MAX(sum_val) FILTER (WHERE {{SUM_IPHONE_CONDITION}}),
        MAX(sum_val)
      ) AS sum_preferred,
      -- sleep: only when picked source covers all five stages. Otherwise
      -- emit NULL for every stage so the existing COALESCE in ON CONFLICT
      -- preserves the prior block atomically (no per-column drift).
      CASE WHEN (SELECT ok FROM sleep_picked_complete)
           THEN MAX(sum_val) FILTER (WHERE source = (SELECT src FROM sleep_picked))
           ELSE NULL
      END AS sum_sleep_resolved
    FROM per_source
    GROUP BY metric_name
)
INSERT INTO daily_scores
    (date, hrv_avg, rhr_avg, sleep_total, sleep_deep, sleep_rem, sleep_core,
     sleep_awake, sleep_unspecified, steps, calories, exercise_min, spo2_avg,
     vo2_avg, resp_avg, computed_at)
SELECT $1,
    MAX(avg_across_sources)  FILTER (WHERE metric_name='heart_rate_variability'),
    MAX(avg_across_sources)  FILTER (WHERE metric_name='resting_heart_rate'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_total'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_deep'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_rem'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_core'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_awake'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_unspecified'),
    MAX(sum_preferred)       FILTER (WHERE metric_name='step_count'),
    MAX(sum_preferred)       FILTER (WHERE metric_name='active_energy'),
    MAX(sum_preferred)       FILTER (WHERE metric_name='apple_exercise_time'),
    MAX(avg_across_sources)  FILTER (WHERE metric_name='blood_oxygen_saturation'),
    MAX(avg_across_sources)  FILTER (WHERE metric_name='vo2_max'),
    MAX(avg_across_sources)  FILTER (WHERE metric_name='respiratory_rate'),
    NOW()::TEXT
FROM agg
ON CONFLICT(date) DO UPDATE SET
    hrv_avg      = COALESCE(EXCLUDED.hrv_avg,      daily_scores.hrv_avg),
    rhr_avg      = COALESCE(EXCLUDED.rhr_avg,      daily_scores.rhr_avg),
    -- Sleep block writes are all-or-nothing per the atomicity gate
    -- (sleep_picked_complete). When the gate passes, EXCLUDED.sleep_total
    -- is non-NULL and we overwrite every stage column — even those that
    -- legitimately become NULL because the picked source is coarse-only
    -- (total + unspecified, no deep/rem/core/awake). Without this, a row
    -- previously populated from a staged device would keep its Apple
    -- Watch deep/REM next to RingConn coarse total — reintroducing the
    -- mixed-source corruption the gate is designed to prevent
    -- (CodeRabbit PR #73). Gate fails ⇒ EXCLUDED.sleep_total IS NULL
    -- ⇒ preserve the prior row as a whole.
    sleep_total       = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_total       ELSE daily_scores.sleep_total       END,
    sleep_deep        = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_deep        ELSE daily_scores.sleep_deep        END,
    sleep_rem         = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_rem         ELSE daily_scores.sleep_rem         END,
    sleep_core        = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_core        ELSE daily_scores.sleep_core        END,
    sleep_awake       = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_awake       ELSE daily_scores.sleep_awake       END,
    sleep_unspecified = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_unspecified ELSE daily_scores.sleep_unspecified END,
    steps        = COALESCE(EXCLUDED.steps,        daily_scores.steps),
    calories     = COALESCE(EXCLUDED.calories,     daily_scores.calories),
    exercise_min = COALESCE(EXCLUDED.exercise_min, daily_scores.exercise_min),
    spo2_avg     = COALESCE(EXCLUDED.spo2_avg,     daily_scores.spo2_avg),
    vo2_avg      = COALESCE(EXCLUDED.vo2_avg,      daily_scores.vo2_avg),
    resp_avg     = COALESCE(EXCLUDED.resp_avg,     daily_scores.resp_avg),
    computed_at  = EXCLUDED.computed_at
