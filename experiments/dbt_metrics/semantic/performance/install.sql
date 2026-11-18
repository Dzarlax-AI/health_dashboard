-- Only executed inside both newly provisioned tenants of the disposable lab.
insert into calendar select (date '2026-10-26'-i)::text from generate_series(0,89) i;
insert into completed_night_sleep(wake_date,duration_hours,source,source_epoch,algorithm_version,input_hash,
    capture_completeness,duration_assessment,finalization_state,claim_eligibility,observed_at)
select (date '2026-10-26'-i)::text, basis.duration_hours,basis.source,basis.source_epoch,basis.algorithm_version,
    i::text,'complete','plausible','final','eligible',(date '2026-10-26'-i)::timestamptz
from generate_series(8,149) i cross join completed_night_sleep basis where basis.wake_date='2026-10-25';
insert into hrv_point(date,source,qty,quality,unit,method,channel,time_kind,start_at)
select c.date||' 02:00:00 +0000', n.source, 50+n.duration_hours::real,'ok','ms','sdnn','xml','timestamp',
       (c.date||' 02:00:00Z')::timestamptz
from calendar c join completed_night_sleep n on n.wake_date=c.date;
insert into hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count)
select 'heart_rate_variability',substring(date,1,13)||':00',source,qty,qty,qty,1 from hrv_point;
insert into daily_scores(date,hrv_avg,sleep_total,rhr_avg)
select c.date,p.qty,n.duration_hours,55 from calendar c join completed_night_sleep n on n.wake_date=c.date
join hrv_point p on substring(p.date,1,10)=c.date;
insert into sleep_interval
select c.date,n.source,(c.date||' 07:00Z')::timestamptz-make_interval(secs=>3600*n.duration_hours),
    (c.date||' 07:00Z')::timestamptz,'sleep_core'
from calendar c join completed_night_sleep n on n.wake_date=c.date;
analyze calendar;
analyze hrv_point;
analyze sleep_interval;
analyze completed_night_sleep;
analyze daily_scores;
analyze hourly_metrics;
