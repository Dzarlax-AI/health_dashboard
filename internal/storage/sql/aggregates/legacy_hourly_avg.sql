INSERT INTO hourly_metrics (metric_name, hour, source, avg_val, min_val, max_val, sample_count)
SELECT metric_name,
       SUBSTRING(date, 1, 13) || ':00' AS hour,
       source,
       AVG(qty), MIN(qty), MAX(qty), COUNT(*)
FROM metric_points
WHERE metric_name = $1 AND qty > 0 AND quality = 'ok' {{SLEEP_DEDUP}} {{FROM_CLAUSE}}
GROUP BY metric_name, SUBSTRING(date, 1, 13) || ':00', source
ON CONFLICT (metric_name, hour, source) DO UPDATE SET
    avg_val=EXCLUDED.avg_val, min_val=EXCLUDED.min_val, max_val=EXCLUDED.max_val,
    sample_count=EXCLUDED.sample_count
