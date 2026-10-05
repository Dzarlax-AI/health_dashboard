SELECT metric_name, hour, source
FROM {{ ref('hourly_metrics') }}
GROUP BY metric_name, hour, source
HAVING COUNT(*) > 1
