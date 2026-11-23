WITH per_source AS ({{DAILY_METRIC_SOURCES}})
SELECT date AS day, {{DAILY_PREFERRED_SUM}} AS source_total
FROM per_source GROUP BY date ORDER BY date
