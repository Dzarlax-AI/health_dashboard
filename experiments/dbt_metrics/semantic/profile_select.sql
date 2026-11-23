-- Operator supplies an already verified, primary-tenant REPEATABLE READ READ ONLY session.
-- This SELECT-only extract contains private rows. Stream to an owner-only temporary file.
-- Include sixty precursor dates for the existing Go sleep selector, and one adjacent
-- day on each side for interval boundaries. Only calendar dates enter denominators.
select 1/((current_setting('transaction_read_only')='on'
    and current_setting('transaction_isolation')='repeatable read'
    and current_schema()='health' and current_user=(select pg_get_userbyid(nspowner)
       from pg_namespace where nspname=current_schema()))::int) as guarded \gset
select json_build_object('table','metadata','row',json_build_array(now(),
  (select value from settings where key='timezone'),current_setting('transaction_read_only')));
select json_build_object('table','calendar','row',json_build_array(d::date::text))
from generate_series(date '2026-08-20', date '2026-11-17', interval '1 day') d;
select json_build_object('table','completed_night_sleep','row',json_build_array(wake_date,duration_hours,
  md5(transaction_timestamp()::text||source),md5(transaction_timestamp()::text||source_epoch),algorithm_version,input_hash,
  capture_completeness,duration_assessment,finalization_state,claim_eligibility))
from completed_night_sleep where wake_date between '2026-06-21' and '2026-11-17';
\if :{?night_card_only}
\else
-- Use an installed, tenant-scoped dbt receipt reader when explicitly supplied.
\if :{?hrv_graph_schema}
with selected as (
  select c.oid,c.reloptions from pg_class c join pg_namespace n on n.oid=c.relnamespace
  where n.nspname=:'hrv_graph_schema' and c.relname='hrv_receipt_points_v1' and c.relkind='v'
), inputs as (
  select distinct d.refobjid as oid from selected s join pg_rewrite r on r.ev_class=s.oid
  join pg_depend d on d.classid='pg_rewrite'::regclass and d.objid=r.oid
  where d.refclassid='pg_class'::regclass and d.refobjid<>s.oid
)
select 1/((exists (select from selected where reloptions @> array['security_invoker=true'])
    and (select count(*)=2 and bool_and(oid in ('metric_points'::regclass,'health_records'::regclass))
         from inputs))::int) as hrv_tenant_guarded \gset
select json_build_object('table','hrv_point','row',json_build_array(date,
 md5(transaction_timestamp()::text||source),qty,quality,unit,method,channel,time_kind,start_at,end_at))
from :"hrv_graph_schema".hrv_receipt_points_v1
where date>='2026-08-19' and date<'2026-11-19';
\else
select json_build_object('table','hrv_point','row',json_build_array(date,md5(transaction_timestamp()::text||coalesce(source,'')),
 qty,quality,coalesce(units,''),'unknown',
 -- The persisted generic key omits the raw HK type. Even an XML transport label
 -- alone cannot distinguish explicit SDNN from a fallback-mapped type.
 case when origin='apple_health_xml' and import_run_id is not null then 'apple_health_xml' else 'live_unattributed' end,
 case when lower(coalesce(h.automation_aggregation,'')) similar to '%(hour|day|minute)%' then 'bucket' else 'timestamp' end,
 date::timestamptz,null))
from metric_points p join health_records h on h.id=p.health_record_id
where metric_name='heart_rate_variability' and date>='2026-08-19' and date<'2026-11-19';
\endif
select json_build_object('table','workout','row',json_build_array(md5(transaction_timestamp()::text||external_id),
  name,start_time,end_time)) from workouts where start_time < '2026-11-19'::timestamptz and end_time >= '2026-08-19'::timestamptz;
select json_build_object('table','daily_hrv','row',json_build_array(date,hrv_avg,repair.key is not null))
from daily_scores left join settings repair on repair.key='cache_maintenance_hrv_repair_v1:' || daily_scores.date
where date between '2026-08-20' and '2026-11-17';
\endif
select json_build_object('table','segment','row',json_build_array(date,md5(transaction_timestamp()::text||coalesce(source,'')),metric_name,qty))
from metric_points where metric_name in ('sleep_deep','sleep_core','sleep_rem','sleep_awake','sleep_unspecified')
and date >= '2026-08-18' and date < '2026-11-19' and quality='ok' and units='hr'
\if :{?night_card_only}
and (qty > 0 or (qty = 0 and (date::timestamptz at time zone 'Europe/Belgrade')::time = '00:00'::time));
\else
and qty > 0;
\endif
select json_build_object('table','period_counts','row',json_build_array(
  (select count(*) from sleep_period_coverage where wake_date between '2026-08-20' and '2026-11-17'),
  (select count(*) from completed_sleep_episode where wake_date between '2026-08-20' and '2026-11-17'),
  (select count(*) from completed_night_sleep where wake_date between '2026-08-20' and '2026-11-17'
    and covered_interval_start is not null and covered_interval_end>covered_interval_start)));
\if :{?night_card_only}
\else
select json_build_object('table','inventory','row',json_build_array(metric_name,coalesce(units,''),count(*)))
from metric_points where date >= '2026-08-20' and date < '2026-11-18' group by metric_name,units;
\endif
rollback;
