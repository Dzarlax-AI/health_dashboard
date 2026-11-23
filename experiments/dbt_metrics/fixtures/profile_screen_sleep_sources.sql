-- Distinct, internally consistent hourly inputs for the six series in the Sleep chart.
insert into source_data.metric_points(metric_name,date,source,qty,quality)
select name,'2026-02-01 05:00:00 +0000','Apple Watch',hours,'ok'
from (values ('sleep_total',7),('sleep_deep',1),('sleep_rem',2),
             ('sleep_core',3.5),('sleep_unspecified',0.5),('sleep_awake',0.25)) points(name,hours);
