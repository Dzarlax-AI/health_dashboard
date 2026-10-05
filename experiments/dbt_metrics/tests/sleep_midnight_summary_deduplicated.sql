SELECT h.metric_name, h.hour, h.source
FROM {{ ref('hourly_metrics') }} h
JOIN {{ source('source_data', 'metric_points') }} p
  ON p.metric_name = h.metric_name AND p.source = h.source
 AND SUBSTRING(p.date, 1, 10) = SUBSTRING(h.hour, 1, 10)
WHERE h.metric_name LIKE 'sleep\_%' ESCAPE '\'
  AND SUBSTRING(p.date, 12, 8) = '00:00:00'
  AND p.qty > 0 AND p.quality = 'ok'
  AND SUBSTRING(h.hour, 12, 5) <> '00:00'
