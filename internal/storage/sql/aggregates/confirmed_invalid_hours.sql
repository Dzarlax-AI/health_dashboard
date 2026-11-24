invalid_hours AS (
    SELECT metric_name, SUBSTRING(date,1,13)||':00' AS hour, source
    FROM hourly_points {{HOUR_FILTER}}
    GROUP BY 1,2,3
    HAVING COUNT(*) FILTER (WHERE qty > 0 AND quality='ok') = 0
), removed AS (
    DELETE FROM hourly_metrics h USING invalid_hours i
    WHERE (h.metric_name,h.hour,h.source)=(i.metric_name,i.hour,i.source)
)
