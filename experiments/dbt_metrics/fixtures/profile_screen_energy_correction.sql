-- Correct one raw HRV observation. Persisted EOD history and observed intraday
-- Energy buckets keep their independent source identity.
SET TIME ZONE 'UTC';
UPDATE metric_points SET qty=42
WHERE source='Energy Screen Apple Watch' AND metric_name='heart_rate_variability'
  AND date=current_date::text || ' 00:00:00 +0000';
