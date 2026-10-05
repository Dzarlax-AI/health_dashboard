{{ config(materialized='incremental', unique_key='date', incremental_strategy='merge') }}
{% do validate_affected_dates() %}

WITH days AS (
    SELECT date FROM {{ source('source_data', 'calendar') }} c
    WHERE {{ affected_dates_filter('c.date') }}
),
per_source AS (
    SELECT SUBSTRING(hour, 1, 10) AS date, metric_name, source,
           AVG(avg_val) AS avg_val,
           SUM(avg_val)::real AS sum_val
    FROM {{ ref('hourly_metrics') }}
    WHERE {{ affected_dates_filter('SUBSTRING(hour, 1, 10)') }}
    GROUP BY SUBSTRING(hour, 1, 10), metric_name, source
),
sleep_totals AS (
    SELECT date, source, sum_val FROM per_source WHERE metric_name = 'sleep_total'
),
sleep_stats AS (
    SELECT date, COUNT(*) AS n_sources, MIN(sum_val) AS min_total, MAX(sum_val) AS max_total
    FROM sleep_totals GROUP BY date
),
sleep_ranked AS (
    SELECT st.date, st.source,
           ROW_NUMBER() OVER (PARTITION BY st.date ORDER BY
             CASE WHEN ss.n_sources > 1 AND ss.min_total > 1.0
                        AND ss.max_total > ss.min_total * 1.4 THEN 0
                  WHEN st.source LIKE '%Ultra%' OR st.source LIKE '%Apple Watch%' THEN 1
                  WHEN st.source LIKE '%RingConn%' THEN 2 ELSE 3 END,
             CASE WHEN ss.n_sources > 1 AND ss.min_total > 1.0
                        AND ss.max_total > ss.min_total * 1.4 THEN st.sum_val END ASC,
             CASE WHEN NOT (ss.n_sources > 1 AND ss.min_total > 1.0
                        AND ss.max_total > ss.min_total * 1.4) THEN st.sum_val END DESC,
             st.source ASC) AS rank
    FROM sleep_totals st JOIN sleep_stats ss USING (date)
),
picked AS (SELECT date, source FROM sleep_ranked WHERE rank = 1),
sleep_gate AS (
    SELECT p.date,
      (ss.n_sources <= 1 OR
       COUNT(DISTINCT ps.metric_name) FILTER (WHERE ps.metric_name IN
         ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake')) = 5 OR
       COUNT(DISTINCT ps.metric_name) FILTER (WHERE ps.metric_name IN
         ('sleep_total','sleep_unspecified')) = 2) AS complete
    FROM picked p JOIN sleep_stats ss USING(date)
    LEFT JOIN per_source ps ON ps.date=p.date AND ps.source=p.source
    GROUP BY p.date, ss.n_sources
),
resolved AS (
    SELECT d.date,
      (SELECT AVG(avg_val)::real FROM per_source WHERE date=d.date AND metric_name='heart_rate_variability') AS hrv_avg,
      (SELECT AVG(avg_val)::real FROM per_source WHERE date=d.date AND metric_name='resting_heart_rate') AS rhr_avg,
      (SELECT sum_val FROM per_source WHERE date=d.date AND metric_name='sleep_total' AND source=(SELECT source FROM picked WHERE date=d.date)) AS picked_sleep_total,
      (SELECT sum_val FROM per_source WHERE date=d.date AND metric_name='sleep_deep' AND source=(SELECT source FROM picked WHERE date=d.date)) AS picked_sleep_deep,
      (SELECT sum_val FROM per_source WHERE date=d.date AND metric_name='sleep_rem' AND source=(SELECT source FROM picked WHERE date=d.date)) AS picked_sleep_rem,
      (SELECT sum_val FROM per_source WHERE date=d.date AND metric_name='sleep_core' AND source=(SELECT source FROM picked WHERE date=d.date)) AS picked_sleep_core,
      (SELECT sum_val FROM per_source WHERE date=d.date AND metric_name='sleep_awake' AND source=(SELECT source FROM picked WHERE date=d.date)) AS picked_sleep_awake,
      (SELECT sum_val FROM per_source WHERE date=d.date AND metric_name='sleep_unspecified' AND source=(SELECT source FROM picked WHERE date=d.date)) AS picked_sleep_unspecified,
      (SELECT COALESCE(MAX(sum_val) FILTER (WHERE source LIKE '%Ultra%' OR source LIKE '%Apple Watch%'), MAX(sum_val) FILTER (WHERE source LIKE '%iPhone%'), MAX(sum_val)) FROM per_source WHERE date=d.date AND metric_name='step_count') AS steps,
      (SELECT COALESCE(MAX(sum_val) FILTER (WHERE source LIKE '%Ultra%' OR source LIKE '%Apple Watch%'), MAX(sum_val) FILTER (WHERE source LIKE '%iPhone%'), MAX(sum_val)) FROM per_source WHERE date=d.date AND metric_name='active_energy') AS calories,
      (SELECT COALESCE(MAX(sum_val) FILTER (WHERE source LIKE '%Ultra%' OR source LIKE '%Apple Watch%'), MAX(sum_val) FILTER (WHERE source LIKE '%iPhone%'), MAX(sum_val)) FROM per_source WHERE date=d.date AND metric_name='apple_exercise_time') AS exercise_min,
      (SELECT AVG(avg_val)::real FROM per_source WHERE date=d.date AND metric_name='blood_oxygen_saturation') AS spo2_avg,
      (SELECT AVG(avg_val)::real FROM per_source WHERE date=d.date AND metric_name='vo2_max') AS vo2_avg,
      (SELECT AVG(avg_val)::real FROM per_source WHERE date=d.date AND metric_name='respiratory_rate') AS resp_avg,
      COALESCE((SELECT complete FROM sleep_gate WHERE date=d.date), false) AS sleep_complete
    FROM days d
)
SELECT r.date,
       COALESCE(r.hrv_avg, prior.hrv_avg)::real AS hrv_avg,
       COALESCE(r.rhr_avg, prior.rhr_avg)::real AS rhr_avg,
       CASE WHEN r.sleep_complete THEN r.picked_sleep_total ELSE prior.sleep_total END::real AS sleep_total,
       CASE WHEN r.sleep_complete THEN r.picked_sleep_deep ELSE prior.sleep_deep END::real AS sleep_deep,
       CASE WHEN r.sleep_complete THEN r.picked_sleep_rem ELSE prior.sleep_rem END::real AS sleep_rem,
       CASE WHEN r.sleep_complete THEN r.picked_sleep_core ELSE prior.sleep_core END::real AS sleep_core,
       CASE WHEN r.sleep_complete THEN r.picked_sleep_awake ELSE prior.sleep_awake END::real AS sleep_awake,
       CASE WHEN r.sleep_complete THEN r.picked_sleep_unspecified ELSE prior.sleep_unspecified END::real AS sleep_unspecified,
       COALESCE(r.steps, prior.steps)::real AS steps,
       COALESCE(r.calories, prior.calories)::real AS calories,
       COALESCE(r.exercise_min, prior.exercise_min)::real AS exercise_min,
       COALESCE(r.spo2_avg, prior.spo2_avg)::real AS spo2_avg,
       COALESCE(r.vo2_avg, prior.vo2_avg)::real AS vo2_avg,
       COALESCE(r.resp_avg, prior.resp_avg)::real AS resp_avg
FROM resolved r
{% if is_incremental() %}
LEFT JOIN {{ this }} prior ON prior.date = r.date
{% else %}
LEFT JOIN (SELECT NULL::text AS date, NULL::real AS hrv_avg, NULL::real AS rhr_avg,
  NULL::real AS sleep_total, NULL::real AS sleep_deep, NULL::real AS sleep_rem,
  NULL::real AS sleep_core, NULL::real AS sleep_awake, NULL::real AS sleep_unspecified,
  NULL::real AS steps, NULL::real AS calories, NULL::real AS exercise_min,
  NULL::real AS spo2_avg, NULL::real AS vo2_avg, NULL::real AS resp_avg) prior ON false
{% endif %}
