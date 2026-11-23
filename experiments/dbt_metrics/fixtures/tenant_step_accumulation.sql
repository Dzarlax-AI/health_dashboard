-- Each recorded hour contains two archive samples in its first minute.
-- The historical wall cache counted the maximum. The physical chart counts both.
-- Its three minute maxima sum to REAL 0x41933333 chronologically and
-- 0x41933334 in reverse. A one-ULP change in the proof changes the selected input.
insert into metric_points(metric_name,date,source,qty,origin)
select 'step_count','2026-01-20 '||lpad(h::text,2,'0')||':'||stamp||' +0000',
       'Apple Watch',amount,'apple_health_xml'
from generate_series(0,23) as h cross join
    (values ('00:01',11.1::real),('00:02',6::real),('01:01',0.15::real),('02:01',7.15::real)) v(stamp,amount);

-- Half the hours also have a late fourth minute. The old triple remains a
-- contiguous chronological slice of the new evidence.
insert into metric_points(metric_name,date,source,qty,origin)
select 'step_count','2026-01-20 '||lpad(h::text,2,'0')||':03:00 +0000',
       'Apple Watch',3::real,'apple_health_xml' from generate_series(12,23) as h;
insert into hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count)
select 'step_count','2026-01-20 '||lpad(h::text,2,'0')||':00','Apple Watch',18.4::real,0.15::real,11.1::real,3
from generate_series(0,23) as h;

-- A larger first minute followed by small fractional minute maxima makes the
-- order dependence measurable under hash and sorted aggregation. The
-- additional six-step reading is a genuine sample below that minute's maximum.
insert into metric_points(metric_name,date,source,qty,origin)
select 'step_count','2026-01-20 '||lpad(h::text,2,'0')||':'||lpad(m::text,2,'0')||':01 +0000',
       'Apple Watch',case when m=0 then 1000 else 0.07003 end::real,'apple_health_xml'
from generate_series(0,11) as h cross join generate_series(0,30) as m
on conflict(metric_name,date,source) do update set qty=excluded.qty;

update hourly_metrics set avg_val=(select sum(val order by m) from (
    select m, case when m=0 then 1000 else 0.07003 end::real as val from generate_series(0,30) m
) q),min_val=0.07003::real,max_val=1000,sample_count=31 where substring(hour,12,2)<'12';
