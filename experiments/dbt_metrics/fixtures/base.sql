INSERT INTO source_data.calendar SELECT to_char(d,'YYYY-MM-DD') FROM generate_series('2026-01-01'::date,'2026-01-15'::date,'1 day') d;
-- Uneven hourly sample counts, two sources, source priority, minute duplicates.
INSERT INTO source_data.metric_points VALUES
('heart_rate_variability','2026-01-01 09:00:00 +0200','Apple Watch',20,'ok'),
('heart_rate_variability','2026-01-01 09:10:00 +0200','Apple Watch',40,'ok'),
('heart_rate_variability','2026-01-01 10:00:00 +0200','Apple Watch',90,'ok'),
('heart_rate_variability','2026-01-01 09:00:00 +0100','RingConn',40,'ok'),
('step_count','2026-01-01 10:01:00 +0200','Apple Watch',10,'ok'),
('step_count','2026-01-01 10:01:30 +0200','Apple Watch',15,'ok'),
('step_count','2026-01-01 10:02:00 +0200','Apple Watch',20,'ok'),
('step_count','2026-01-01 10:00:00 +0200','iPhone',100,'ok'),
('step_count','2026-01-01 10:00:00 +0200','Other',1000,'ok'),
('step_count','2026-01-01 11:00:00 +0200','Apple Watch',0,'ok'),
('step_count','2026-01-01 11:01:00 +0200','Apple Watch',-1,'ok'),
('step_count','2026-01-01 11:02:00 +0200','Apple Watch',100,'invalid'),
('resting_heart_rate','2026-01-01 09:00:00 +0200','Apple Watch',60,'ok'),
('active_energy','2026-01-01 10:00:00 +0200','Apple Watch',120,'ok'),
('apple_exercise_time','2026-01-01 10:00:00 +0200','Apple Watch',30,'ok'),
('blood_oxygen_saturation','2026-01-01 10:00:00 +0200','Apple Watch',98,'ok'),
('vo2_max','2026-01-01 10:00:00 +0200','Apple Watch',45,'ok'),
('respiratory_rate','2026-01-01 10:00:00 +0200','Apple Watch',16,'ok'),
('night_sleep_total','2026-01-01 10:01:00 +0200','Apple Watch',7,'ok'),
('nap_total','2026-01-01 10:01:00 +0200','Apple Watch',1,'ok');
-- Same staged night across days. Source totals cover floor, threshold, ties,
-- conservative minimum, incomplete pick, and coarse-only pick.
INSERT INTO source_data.metric_points
SELECT m,'2026-01-'||lpad(d::text,2,'0')||' 00:00:00 +0100','Apple Watch',v,'ok'
FROM generate_series(1,12) d CROSS JOIN (VALUES
 ('sleep_total',7::real),('sleep_deep',1::real),('sleep_rem',2::real),
 ('sleep_core',4::real),('sleep_awake',0.5::real)) s(m,v);
INSERT INTO source_data.metric_points VALUES
('sleep_total','2026-01-01 02:00:00 +0100','Apple Watch',3,'ok'),
('sleep_total','2026-01-01 03:00:00 +0100','Apple Watch',4,'ok'),
('sleep_deep','2026-01-01 02:00:00 +0100','Apple Watch',8,'invalid'),
('sleep_total','2026-01-02 00:00:00 +0100','RingConn',0.5,'ok'),
('sleep_total','2026-01-03 00:00:00 +0100','RingConn',1,'ok'),
('sleep_unspecified','2026-01-03 00:00:00 +0100','RingConn',1,'ok'),
('sleep_total','2026-01-04 00:00:00 +0100','RingConn',5,'ok'),
('sleep_unspecified','2026-01-04 00:00:00 +0100','RingConn',5,'ok'),
('sleep_total','2026-01-05 00:00:00 +0100','RingConn',4,'ok'),
('sleep_unspecified','2026-01-05 00:00:00 +0100','RingConn',4,'ok'),
('sleep_total','2026-01-06 00:00:00 +0100','RingConn',4,'ok'),
('sleep_total','2026-01-07 00:00:00 +0100','RingConn',7,'ok'),
('sleep_unspecified','2026-01-07 00:00:00 +0100','RingConn',7,'ok'),
('sleep_total','2026-01-13 02:01:00 +0100','Other',2,'ok'),
('sleep_total','2026-01-13 02:01:30 +0100','Other',3,'ok'),
('sleep_total','2026-01-13 03:00:00 +0100','Other',4,'ok'),
('sleep_total','2026-01-13 00:00:00 +0100','Other',99,'invalid');
-- Exact 1.4 ratio (float4-representable values), then just above it.
UPDATE source_data.metric_points SET qty=5 WHERE metric_name='sleep_total' AND SUBSTRING(date,1,10)='2026-01-04' AND source='RingConn';
-- Equal highest Watch sources tie by source name; all stages stay together.
INSERT INTO source_data.metric_points SELECT metric_name,replace(date,'2026-01-07','2026-01-08'),'Apple Watch B',qty,'ok' FROM source_data.metric_points WHERE SUBSTRING(date,1,10)='2026-01-07' AND source='Apple Watch';
UPDATE source_data.metric_points SET qty=3 WHERE SUBSTRING(date,1,10)='2026-01-08' AND source='Apple Watch B' AND metric_name='sleep_rem';
INSERT INTO source_data.metric_points VALUES
('sleep_total','2026-01-09 00:00:00 +0100','RingConn',4.999,'ok'),
('sleep_unspecified','2026-01-09 00:00:00 +0100','RingConn',4.999,'ok'),
('sleep_custom','2026-01-10 00:00:00 +0100','Other',0,'ok'),
('sleep_custom','2026-01-10 02:00:00 +0100','Other',2,'ok'),
('sleep_total','2026-01-10 00:00:00 +0100','RingConn',4,'ok'),
('sleep_unspecified','2026-01-10 00:00:00 +0100','RingConn',4,'ok'),
('sleep_total','2026-01-10 00:00:00 +0100','A Other',4,'ok'),
('sleep_unspecified','2026-01-10 00:00:00 +0100','A Other',3.5,'ok'),
('heart_rate_variability','2026-01-11 09:00:00 +0100','Other',12.123456,'ok'),
('heart_rate_variability','2026-01-11 09:01:00 +0100','Other',20.765432,'ok');
-- Two-stage AVG must retain double precision until the final REAL write.
INSERT INTO source_data.metric_points VALUES
('resting_heart_rate','2026-01-11 09:00:00 +0100','A',1,'ok'),
('resting_heart_rate','2026-01-11 10:00:00 +0100','A',1.0000001,'ok'),
('resting_heart_rate','2026-01-11 09:00:00 +0100','B',1.0000001,'ok');
UPDATE source_data.metric_points SET qty=0 WHERE SUBSTRING(date,1,10)='2026-01-12' AND metric_name='sleep_awake';
