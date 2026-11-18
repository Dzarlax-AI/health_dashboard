-- Sixty lookback days plus ninety measured dates across the Belgrade autumn DST transition.
insert into source_data.calendar select d::date::text
from generate_series(date '2026-08-20',date '2026-11-17',interval '1 day') d;
insert into source_data.completed_night_sleep
select wake_day, 6.5+(i%4)*0.25, 'watch', case when i<75 then 'epoch1' else 'epoch2' end,
 'v1',md5(wake_day),case when i%15=0 then 'partial' else 'complete' end,'plausible',
 case when i%18=0 then 'provisional' else 'final' end,
 case when i%18=0 or i%15=0 then 'ineligible' else 'eligible' end
from (select d::date::text as wake_day,row_number() over(order by d) i from
 generate_series(date '2026-06-21',date '2026-11-17',interval '1 day') d) dates where i%17<>0;
insert into source_data.sleep_interval
select wake_date, source, wake_date::date+time '06:30'-duration_hours*interval '1 hour',
 wake_date::date+time '06:30','sleep_core' from source_data.completed_night_sleep
where capture_completeness='complete' and extract(day from wake_date::date)::int%7<>0;
insert into source_data.sleep_stage_day select wake_date,source,'sleep_core' from source_data.completed_night_sleep;
insert into source_data.workout
select date,'running',date::date+time '12:00',date::date+time '13:00' from source_data.calendar
where extract(day from date::date)::int%2=0;
insert into source_data.hrv_point
select to_char(ts::timestamptz,'YYYY-MM-DD HH24:MI:SS OF'), device, val,'ok','ms',
 case when extract(day from ts)::int%4=0 then 'unknown' else 'sdnn' end,'synthetic',
 case when extract(day from ts)::int%3=0 then 'interval' else 'timestamp' end,
 ts,case when extract(day from ts)::int%3=0 then ts+interval '45 seconds' end
from (select c.date::date+t as ts,device,val from source_data.calendar c
cross join (values (time '02:00','a',20),(time '02:05','a',20),(time '02:10','a',20),
 (time '12:15','a',80),(time '02:01','b',100)) seed(t,device,val)
where extract(day from date::date)::int%11<>0) samples;
insert into source_data.daily_hrv select date, 75 from source_data.calendar;
