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
), counts as (
    select id,period,source,count(*) as candidates,
           count(*) filter(where decision='interval') as interval_points,
           count(*) filter(where decision='timestamp') as timestamp_points,
           count(*) filter(where not inside_active_workout and time_kind='timestamp' and analytic_quality and unit='ms' and ordinal=1 and min_qty=max_qty) as all_timestamp_candidates,
           count(*) filter(where not inside_active_workout and time_kind='timestamp' and analytic_quality and unit='ms' and ordinal=1 and min_qty=max_qty and window_count=1) as unique_window_timestamp_candidates,
           count(*) filter(where decision='unknown_method') as unknown_method_points,
           count(*) filter(where decision='other_method') as other_method_points,
           count(*) filter(where decision='time_precision') as imprecise_points,
           count(*) filter(where decision='ambiguous_workout') as ambiguous_points,
           count(*) filter(where decision='invalid_quality') as invalid_points,
           count(*) filter(where decision='unsupported_unit') as unsupported_unit_points,
           count(*) filter(where decision='crosses_boundary') as boundary_points,
           count(*) filter(where decision='active_workout') as active_workout_points,
           count(*) filter(where decision in ('duplicate','conflicting_duplicate')) as duplicate_points,
           avg(qty) filter(where decision='interval') as interval_mean_ms,
           avg(qty) filter(where decision='timestamp') as timestamp_mean_ms,
           string_agg(distinct decision,',' order by decision) as decisions
    from {{ ref('semantic_hrv_workout_evidence_v1') }} group by id,period,source
)
select w.id,w.kind,w.start_at,w.end_at,w.period,s.source,
       coalesce(s.candidates,0) as candidates,coalesce(s.interval_points,0) as interval_points,
       coalesce(s.timestamp_points,0) as timestamp_points,coalesce(s.all_timestamp_candidates,0) as all_timestamp_candidates,
       coalesce(s.unique_window_timestamp_candidates,0) as unique_window_timestamp_candidates,
       coalesce(s.unknown_method_points,0) as unknown_method_points,coalesce(s.other_method_points,0) as other_method_points,
       coalesce(s.imprecise_points,0) as imprecise_points,coalesce(s.ambiguous_points,0) as ambiguous_points,
       coalesce(s.invalid_points,0) as invalid_points,coalesce(s.unsupported_unit_points,0) as unsupported_unit_points,
       coalesce(s.boundary_points,0) as boundary_points,coalesce(s.active_workout_points,0) as active_workout_points,
       coalesce(s.duplicate_points,0) as duplicate_points,s.interval_mean_ms,s.timestamp_mean_ms,
       case when w.bounds is null then 'invalid_workout_interval' when s.interval_points>0 then 'measured_interval'
            when s.timestamp_points>0 then 'timestamp_only' else coalesce(s.decisions,'no_observations') end as reason,
       s.decisions
from windows w
{% if not var('tenant_native_catalog', false) %}
join {{ source('source_data','calendar') }} c
  on c.date=(w.start_at at time zone '{{ var("semantic_timezone","Europe/Belgrade") }}')::date::text
{% endif %}
left join counts s on s.id=w.id and s.period=w.period
