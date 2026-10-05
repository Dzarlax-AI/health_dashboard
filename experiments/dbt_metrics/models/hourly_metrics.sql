{{ config(materialized='incremental', incremental_strategy='append', pre_hook="{{ delete_hourly_affected_days() }}") }}
{% do validate_affected_dates() %}

WITH valid_points AS (
    SELECT metric_name, date, source, qty::real AS qty
    FROM {{ source('source_data', 'metric_points') }} p
    WHERE qty > 0 AND quality = 'ok'
      AND {{ affected_dates_filter('SUBSTRING(date, 1, 10)') }}
      AND NOT (
        metric_name LIKE 'sleep\_%' ESCAPE '\'
        AND SUBSTRING(date, 12, 8) <> '00:00:00'
        AND EXISTS (
          SELECT 1 FROM {{ source('source_data', 'metric_points') }} midnight
          WHERE midnight.metric_name = p.metric_name
            AND SUBSTRING(midnight.date, 1, 10) = SUBSTRING(p.date, 1, 10)
            AND midnight.source = p.source
            AND SUBSTRING(midnight.date, 12, 8) = '00:00:00'
            AND midnight.qty > 0 AND midnight.quality = 'ok'
        )
      )
),
minute_values AS (
    SELECT metric_name, source,
           SUBSTRING(date, 1, 13) || ':00' AS hour,
           SUBSTRING(date, 1, 16) AS minute,
           MAX(qty)::real AS minute_max,
           MIN(qty)::real AS minute_min
    FROM valid_points
    WHERE metric_name LIKE 'sleep\_%' ESCAPE '\' OR metric_name = ANY({{ sum_metrics_array() }})
    GROUP BY metric_name, source, SUBSTRING(date, 1, 13), SUBSTRING(date, 1, 16)
),
sum_rows AS (
    SELECT metric_name, hour, source,
           SUM(minute_max)::real AS avg_val,
           MIN(minute_min)::real AS min_val,
           MAX(minute_max)::real AS max_val,
           COUNT(*)::integer AS sample_count
    FROM minute_values GROUP BY metric_name, hour, source
),
avg_rows AS (
    SELECT metric_name, SUBSTRING(date, 1, 13) || ':00' AS hour, source,
           AVG(qty)::real AS avg_val, MIN(qty)::real AS min_val,
           MAX(qty)::real AS max_val, COUNT(*)::integer AS sample_count
    FROM valid_points
    WHERE metric_name NOT LIKE 'sleep\_%' ESCAPE '\'
      AND metric_name <> ALL({{ sum_metrics_array() }})
    GROUP BY metric_name, SUBSTRING(date, 1, 13), source
)
SELECT * FROM avg_rows
UNION ALL
SELECT * FROM sum_rows
