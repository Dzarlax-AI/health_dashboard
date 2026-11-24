{{ config(enabled=var('semantic_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

-- Context workouts beyond the evaluation dates still participate in ambiguous
-- assignment. Invalid episodes remain in the denominator with an explicit reason.
with workouts as (
    select *,case when end_at>start_at then tstzrange(start_at,end_at,'[)') end as active
    {% if var('tenant_native_catalog', false) %}
    from (select id::text as id,name as kind,start_time as start_at,end_time as end_at
          from {{ source('archive_records','workouts') }}) persisted_workouts
    {% else %}from {{ source('source_data','workout') }}{% endif %}
), windows as (
    select id,kind,start_at,end_at,period,
           case when period='during' then active
                when active is not null then tstzrange(end_at,end_at+interval '30 minutes','[)') end as bounds
    from workouts cross join (values ('during'),('after_30m')) as k(period)
), candidates as (
    select w.id,w.period,p.date,p.source,p.channel,p.method,p.qty,p.writer_valid,p.analytic_quality,p.unit,p.ordinal,p.min_qty,p.max_qty,p.measurement,p.start_at,p.time_kind,p.analytic_status,
           w.bounds, count(*) over(partition by w.period,p.date,p.source) as window_count
    from windows w join {{ ref('semantic_hrv_observation_v1') }} p
      on (p.time_kind='interval' and w.bounds && p.measurement)
      or (p.time_kind='timestamp' and w.bounds @> p.start_at)
      or (p.time_kind='bucket' and (w.bounds && p.bucket or (p.bucket is null and w.bounds @> p.start_at)))
), assigned as (
    select c.*, period='after_30m' and exists(
               select 1 from workouts w where w.active && c.measurement
                  or (c.time_kind='timestamp' and w.active @> c.start_at)
           ) as inside_active_workout
    from candidates c
), decisions as (
    select a.*, case
        when inside_active_workout then 'active_workout'
        when time_kind='bucket' then 'time_precision'
        when analytic_status<>'eligible' then analytic_status
        when window_count>1 then 'ambiguous_workout'
        when time_kind='interval' and not(bounds @> measurement) then 'crosses_boundary'
        else time_kind end as decision
    from assigned a
)
select * from decisions
