-- Independently populated old caches make the reader switch observable.
INSERT INTO daily_scores(date,hrv_avg,vo2_avg,steps)
SELECT d::date::text,84,33,903
FROM generate_series('2026-01-22'::date,'2026-02-01'::date,'1 day') d;
INSERT INTO hourly_metrics
SELECT m.name,d::date::text || ' 09:00','Apple Watch',m.qty,m.qty,m.qty,1
FROM generate_series('2026-01-22'::date,'2026-02-01'::date,'1 day') d
CROSS JOIN (VALUES ('step_count',903), ('vo2_max',33), ('heart_rate_variability',84)) m(name,qty);
INSERT INTO hourly_metrics VALUES ('body_mass','2026-02-02 09:00','Apple Watch',75,75,75,1);
UPDATE daily_scores SET readiness=61,score_version=3 WHERE date='2026-02-01';
