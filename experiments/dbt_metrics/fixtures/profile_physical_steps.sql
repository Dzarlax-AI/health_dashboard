INSERT INTO calendar(date) VALUES ('2024-10-27'), ('2024-10-28') ON CONFLICT DO NOTHING;
INSERT INTO metric_points(metric_name,date,source,qty,quality,health_record_id,units) VALUES
 ('step_count','2024-10-27 02:00:00 +0200','Apple Watch',40,'ok',1,'count'),
 ('step_count','2024-10-27 02:00:00 +0100','iPhone',70,'ok',1,'count'),
 ('step_count','2024-10-28 03:00:00 +0100','Apple Watch',0,'ok',1,'count');
