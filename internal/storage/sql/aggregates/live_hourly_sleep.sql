INSERT INTO hourly_metrics (metric_name, hour, source, avg_val, min_val, max_val, sample_count)
WITH hourly_points AS (
    SELECT metric_name, date, source, qty, quality FROM metric_points
    WHERE SUBSTRING(date,1,10) = $1 AND metric_name LIKE 'sleep\_%' ESCAPE '\' {{SLEEP_DEDUP}}
)
{{HOURLY_SUM_ROWS}}
ON CONFLICT (metric_name, hour, source) DO UPDATE SET
    avg_val=EXCLUDED.avg_val, min_val=EXCLUDED.min_val, max_val=EXCLUDED.max_val,
    sample_count=EXCLUDED.sample_count
