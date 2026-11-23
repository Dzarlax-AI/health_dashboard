WITH per_source AS (
    {{DAILY_METRIC_SOURCES}}
)
SELECT date, {{DAILY_SOURCE_MEAN}} AS avg_val FROM per_source GROUP BY date
