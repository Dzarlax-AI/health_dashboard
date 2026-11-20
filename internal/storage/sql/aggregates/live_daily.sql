WITH per_source AS (
    SELECT metric_name, source,
           AVG(avg_val) AS avg_val,
           SUM(avg_val) AS sum_val
    FROM hourly_metrics
    WHERE SUBSTRING(hour,1,10) = $1
    GROUP BY metric_name, source
),
{{SLEEP_SELECTION_CTES}},
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
