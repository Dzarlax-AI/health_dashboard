INSERT INTO hourly_metrics (metric_name, hour, source, avg_val, min_val, max_val, sample_count)
		SELECT metric_name, hour, source,
		       SUM(minute_max), MIN(minute_min), MAX(minute_max), COUNT(*)
		FROM (
			SELECT metric_name, source,
			       SUBSTRING(date, 1, 13) || ':00' AS hour,
			       SUBSTRING(date, 1, 16) AS minute,
			       MAX(qty) AS minute_max, MIN(qty) AS minute_min
			FROM metric_points
			WHERE SUBSTRING(date,1,10) = $1
			  AND qty > 0
			  AND quality = 'ok'
			  AND metric_name LIKE 'sleep\_%' ESCAPE '\'
			  {{SLEEP_DEDUP}}
			GROUP BY metric_name, source,
			         SUBSTRING(date, 1, 13) || ':00',
			         SUBSTRING(date, 1, 16)
		) sub
		GROUP BY metric_name, hour, source
		ON CONFLICT (metric_name, hour, source) DO UPDATE SET
			avg_val=EXCLUDED.avg_val, min_val=EXCLUDED.min_val, max_val=EXCLUDED.max_val,
			sample_count=EXCLUDED.sample_count
