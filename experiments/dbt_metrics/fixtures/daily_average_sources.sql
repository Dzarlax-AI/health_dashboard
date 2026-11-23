INSERT INTO source_data.metric_points
SELECT metric, '2026-01-15 ' || hour, source, base + delta, 'ok'
FROM (VALUES ('resting_heart_rate',60),('blood_oxygen_saturation',90),
             ('vo2_max',35),('respiratory_rate',12)) m(metric,base)
CROSS JOIN (VALUES ('09:00:00','A',0),('10:00:00','A',2),('09:00:00','B',7)) x(hour,source,delta);
