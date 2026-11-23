-- A complete rolling recovery window and three observations for the section cards.
-- Feb 1 steps are supplied by the existing late-correction fixture.
INSERT INTO source_data.calendar
SELECT d::date::text FROM generate_series('2026-01-22'::date,'2026-02-02'::date,'1 day') d
ON CONFLICT DO NOTHING;
INSERT INTO source_data.metric_points(metric_name,date,source,qty,quality)
SELECT m.name, d::date::text || ' 09:00:00 +0000','Apple Watch',m.qty,'ok'
FROM generate_series('2026-01-22'::date,'2026-02-01'::date,'1 day') d
CROSS JOIN (VALUES ('step_count',22), ('vo2_max',46), ('heart_rate_variability',52)) m(name,qty)
WHERE d::date <> date '2026-02-01' OR m.name <> 'step_count';
-- This observation fixes the briefing date and avoids replacing a prior day's
-- cached section metrics with raw measurements for the current morning.
INSERT INTO source_data.metric_points(metric_name,date,source,qty,quality)
VALUES ('body_mass','2026-02-02 09:00:00 +0000','Apple Watch',75,'ok');

-- Morning overlap contributes 35 Watch steps. This phone-only hour adds 100.
INSERT INTO source_data.metric_points(metric_name,date,source,qty,quality)
VALUES ('step_count','2026-01-01 18:00:00 +0200','iPhone',100,'ok');
