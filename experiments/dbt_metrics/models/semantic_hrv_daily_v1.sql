{{ config(enabled=var('semantic_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with points as (
    select * from {{ ref('semantic_hrv_observation_v1') }}
), hours as (
    select writer_date, source, substring(date,1,13) as hour, avg(qty)::real as hour_ms
    from points where writer_valid group by 1,2,3
), devices as (
    select writer_date,source,avg(hour_ms) as device_ms from hours group by 1,2
), live as (
    select writer_date,avg(device_ms)::real as live_ms from devices group by 1
), legacy as (
    select writer_date,avg(hour_ms)::real as legacy_ms from hours group by 1
), raw as (
    select writer_date, avg(qty) filter(where writer_valid) as raw_ms,
           count(*) filter(where writer_valid) as n_points,
           count(*) filter(where analytic_quality and unit='ms' and method='sdnn') as sdnn_points,
           count(*) filter(where writer_valid and method='unknown') as unknown_method_points,
           count(*) filter(where writer_valid and method='rmssd') as rmssd_points,
           -- These are the input points to the historical personal baseline, grouped
           -- by writer date. The baseline's subsequent multi-day robust fitting is separate.
           count(*) filter(where quality='ok' and (substring(date,12,2)>='23' or substring(date,12,2)<'09')) as fixed_window_points,
           percentile_cont(.5) within group(order by qty)
               filter(where quality='ok' and (substring(date,12,2)>='23' or substring(date,12,2)<'09')) as fixed_window_median_ms
    from points group by 1
)
select c.date,l.live_ms,h.legacy_ms,r.raw_ms,coalesce(r.n_points,0) as n_points,
       coalesce(r.sdnn_points,0) as sdnn_points,coalesce(r.unknown_method_points,0) as unknown_method_points,
       coalesce(r.rmssd_points,0) as rmssd_points,
       coalesce(r.fixed_window_points,0) as fixed_window_points,r.fixed_window_median_ms,
       cache.hrv_avg as persisted_ms,
       coalesce(r.raw_ms,cache.hrv_avg) as morning_ms,
       case when r.raw_ms is not null then 'raw' when cache.hrv_avg is not null then 'same_day_cache'
            else 'missing_same_day_value' end as morning_origin,
       case when coalesce(r.raw_ms,cache.hrv_avg) is not null then c.date end as morning_observed_on,
       case when r.n_points>=4 then 'final' when coalesce(r.raw_ms,cache.hrv_avg) is not null then 'provisional'
            else 'missing' end as morning_confidence
from {{ source('source_data','calendar') }} c
left join live l on l.writer_date=c.date
left join legacy h on h.writer_date=c.date
left join raw r on r.writer_date=c.date
left join {{ source('source_data','daily_hrv') }} cache on cache.date=c.date
