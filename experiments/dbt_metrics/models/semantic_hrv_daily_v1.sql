{{ config(enabled=var('semantic_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with cache as (
    select date, hrv_avg, case when not repair_pending then hrv_avg end as fallback_ms
    from {{ source('source_data','daily_hrv') }}
), points as (
    select * from {{ ref('semantic_hrv_observation_v1') }}
), hours as (
    select 'heart_rate_variability'::text as metric_name, source, substring(date,1,13) as hour,
           avg(qty)::real as avg_val, min(qty) as min_val, max(qty) as max_val
    from points where writer_valid group by source, substring(date,1,13)
), devices as (
    {{ daily_source_rows('hours', 'true') }}
), daily as (
    select date, {{ daily_source_mean() }} as avg_val from devices group by date
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
select c.date,d.avg_val as live_ms,d.avg_val as legacy_ms,r.raw_ms,coalesce(r.n_points,0) as n_points,
       coalesce(r.sdnn_points,0) as sdnn_points,coalesce(r.unknown_method_points,0) as unknown_method_points,
       coalesce(r.rmssd_points,0) as rmssd_points,
       coalesce(r.fixed_window_points,0) as fixed_window_points,r.fixed_window_median_ms,
       cache.hrv_avg as persisted_ms,
       coalesce(r.raw_ms,cache.fallback_ms) as morning_ms,
       case when r.raw_ms is not null then 'raw' when cache.fallback_ms is not null then 'same_day_cache'
            else 'missing_same_day_value' end as morning_origin,
       case when coalesce(r.raw_ms,cache.fallback_ms) is not null then c.date end as morning_observed_on,
       case when r.n_points>=4 then 'final' when coalesce(r.raw_ms,cache.fallback_ms) is not null then 'provisional'
            else 'missing' end as morning_confidence
from {{ source('source_data','calendar') }} c
left join daily d on d.date=c.date
left join raw r on r.writer_date=c.date
left join cache on cache.date=c.date
