SELECT SUBSTRING(hour,1,10), AVG(avg_val)
FROM hourly_metrics
WHERE metric_name = $1 {{FROM_CLAUSE}}
GROUP BY SUBSTRING(hour,1,10)
