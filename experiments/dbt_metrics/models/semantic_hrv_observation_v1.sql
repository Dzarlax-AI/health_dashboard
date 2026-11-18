{{ config(enabled=var('semantic_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

-- Keep the original date/offset for the Go compatibility paths. On analytic paths,
-- identical instants from the same source/method are one physical candidate.
with classified as (
    select p.*, coalesce(quality='ok' and qty>0,false) as writer_valid,
           coalesce(quality='ok' and qty between 4 and 300,false) as analytic_quality
    from {{ source('source_data','hrv_point') }} p
), points as (
    select p.*, substring(date,1,10) as writer_date,
           case when time_kind = 'interval' then tstzrange(start_at,end_at,'[)') end as measurement,
           case when time_kind='bucket' and end_at>start_at then tstzrange(start_at,end_at,'[)') end as bucket,
           count(*) filter (where analytic_quality and unit='ms') over (candidate) as copies,
           min(qty) filter (where analytic_quality and unit='ms') over (candidate) as min_qty,
           max(qty) filter (where analytic_quality and unit='ms') over (candidate) as max_qty,
           row_number() over (candidate order by (analytic_quality and unit='ms') desc nulls last, date) as ordinal
    from classified p
    window candidate as (partition by source, method, time_kind, start_at,
                            case when time_kind='interval' then end_at end,
                            case when start_at is null then date end)
)
select *, case when not analytic_quality then 'invalid_quality'
               when unit <> 'ms' then 'unsupported_unit'
               when method='unknown' then 'unknown_method'
               when method<>'sdnn' then 'other_method'
               when time_kind not in ('interval','timestamp') then 'time_precision'
               when min_qty <> max_qty then 'conflicting_duplicate'
               when ordinal > 1 then 'duplicate'
               else 'eligible' end as analytic_status
from points
