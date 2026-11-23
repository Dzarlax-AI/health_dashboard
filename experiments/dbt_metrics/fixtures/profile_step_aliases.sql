insert into source_data.calendar
select day::date::text from generate_series(date '2026-01-16',date '2026-01-21',interval '1 day') day
on conflict do nothing;

insert into source_data.metric_points(metric_name,date,source,qty,quality,units) values
 ('step_count','2026-01-16 23:15:00 +0000','Watch',21,'ok','count'),
 ('step_count','2026-01-17 01:15:00 +0200','Watch',21,'ok','count'),
 ('step_count','2026-01-18 23:20:00 +0000','Watch',37,'ok','count'),
 ('step_count','2026-01-20 09:15:00 +0000','Watch',3,'ok','count'),
 ('step_count','2026-01-20 11:15:00 +0200','Watch',4,'ok','count'),
 ('step_count','2026-01-21 09:00:00','Watch',5,'ok','count'),
 ('step_count','2026-01-21 11:00:00 +0200','Watch',5,'ok','count');
