{{ config(enabled=var('semantic_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

-- The episode adapter associates real intervals to a canonical source and wake
-- date. No clock-of-day or capture-window fallback is permitted here.
with timeline as (
    select i.*,
           max(end_at) over (partition by wake_date order by start_at,end_at,stage
               rows between unbounded preceding and 1 preceding) as previous_covered_end,
           lag(stage) over w as previous_stage, lead(stage) over w as next_stage,
           lag(end_at) over w as previous_end, lead(start_at) over w as next_start,
           extract(epoch from end_at-start_at)::float8 / 60 as minutes
    from {{ ref('semantic_sleep_intervals_v1') }} i
    window w as (partition by wake_date order by start_at,end_at,stage)
), episode as (
    select wake_date, min(start_at) as episode_start, max(end_at) as episode_end,
           bool_or(previous_covered_end > start_at) as has_conflict,
           sum(case when start_at > previous_covered_end
                    then extract(epoch from start_at-previous_covered_end)::float8/60 else 0 end) as gap_minutes,
           bool_or(stage='awake' and (previous_stage is null or next_stage is null)) as open_awake,
           count(*) filter (where stage='awake' and previous_stage not in ('awake') and next_stage not in ('awake')
               and start_at=previous_end and end_at=next_start) as internal_bouts,
           sum(minutes) filter (where stage<>'awake') as asleep,
           sum(minutes) filter (where stage='deep') as deep, sum(minutes) filter (where stage='rem') as rem,
           sum(minutes) filter (where stage='core') as core,
           sum(minutes) filter (where stage='unspecified') as unspecified,
           sum(minutes) filter (where stage='awake') as awake,
           jsonb_agg(jsonb_build_array(extract(epoch from start_at),extract(epoch from end_at),stage)
                     order by start_at,end_at,stage) as version_inputs
    from timeline group by wake_date
), summary as (
    select date, source,
           bool_or(observed_minutes is null) as unknown_quantity,
           sum(observed_minutes) filter (where metric_name<>'sleep_awake') as asleep,
           max(observed_minutes) filter (where metric_name='sleep_deep') as deep,
           max(observed_minutes) filter (where metric_name='sleep_rem') as rem,
           max(observed_minutes) filter (where metric_name='sleep_core') as core,
           max(observed_minutes) filter (where metric_name='sleep_unspecified') as unspecified,
           max(observed_minutes) filter (where metric_name='sleep_awake') as awake,
           jsonb_agg(jsonb_build_array(metric_name,observed_minutes) order by metric_name) as version_inputs
    from {{ ref('native_sleep_stage_day_v1') if var('tenant_native_catalog',false) else source('source_data','sleep_stage_day') }}
    where metric_name in ('sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake')
    group by date,source
), input as (
    select c.date as wake_date, n.source, n.capture_completeness, n.finalization_state,
           n.duration_hours*60 as duration_minutes, e.episode_start,e.episode_end,e.gap_minutes,
           e.open_awake,e.internal_bouts, e.deep,e.rem,e.core,e.unspecified,e.awake,e.asleep,
           s.deep as day_deep,s.rem as day_rem,s.core as day_core,s.unspecified as day_unspecified,
           s.awake as day_awake,s.asleep as day_asleep,
           case when n.wake_date is null then 'no_canonical_night'
                when n.capture_completeness <> 'complete' then 'capture_incomplete'
                when n.duration_assessment <> 'plausible' or not (n.duration_hours>0 and n.duration_hours<=24)
                    then 'duration_not_plausible'
                when e.has_conflict then 'overlapping_stage_labels'
                when e.wake_date is not null and (e.asleep is null or abs(e.asleep-n.duration_hours*60)>1)
                    then 'interval_duration_mismatch'
                when e.wake_date is not null then 'recorded_episode'
                when s.date is null then 'no_same_source_stage_evidence'
                when s.unknown_quantity or s.asleep is null then 'unknown_summary_quantity'
                when abs(s.asleep-n.duration_hours*60)>1 then 'day_duration_mismatch'
                else 'matched_day_summary' end as stage_reason,
           encode(sha256(convert_to(jsonb_build_array('semantic_sleep_continuity_v1', c.date,n.source,n.input_hash,
               n.source_epoch,n.algorithm_version,n.capture_completeness,n.finalization_state,
               n.duration_assessment,n.duration_hours,e.version_inputs,s.version_inputs)::text,'UTF8')),'hex') as input_version
    from {{ ref('native_night_calendar_v1') if var('tenant_native_catalog',false) else source('source_data','calendar') }} c
    left join {{ source('source_data','completed_night_sleep') }} n on n.wake_date=c.date
    left join episode e on e.wake_date=c.date
    left join summary s on s.date=c.date and s.source=n.source
), picked as (
    select wake_date,source,capture_completeness,finalization_state,duration_minutes,stage_reason,input_version,
           case stage_reason when 'recorded_episode' then 'episode_intervals'
               when 'matched_day_summary' then 'same_source_day_summary' else 'unavailable' end as stage_basis,
           case stage_reason when 'recorded_episode' then asleep when 'matched_day_summary' then day_asleep end as recorded_asleep_minutes,
           {% for field in ['deep','rem','core','unspecified','awake'] %}
           case stage_reason when 'recorded_episode' then {{ field }}
                when 'matched_day_summary' then day_{{ field }} end as {{ field }}_minutes,
           {% endfor %}
           case when stage_reason='recorded_episode' then episode_start end as episode_start,
           case when stage_reason='recorded_episode' then episode_end end as episode_end,
           case when stage_reason='recorded_episode' then gap_minutes end as gap_minutes,
           case when stage_reason='recorded_episode' then internal_bouts end as observed_internal_awake_bouts,
           case when stage_reason='matched_day_summary' then 'daily_sum_has_no_timing'
                when stage_reason<>'recorded_episode' then stage_reason
                when gap_minutes>0 then 'gaps_in_episode'
                when open_awake then 'open_awake_boundary'
                when deep is null and rem is null and core is null then 'coarse_sleep_only'
                else 'continuous_recording' end as continuity_reason
    from input
)
select *,
       {% for field in ['deep','rem','core','unspecified'] %}
       {{ field }}_minutes / nullif(recorded_asleep_minutes,0) as {{ field }}_fraction,
       {% endfor %}
       case when continuity_reason='continuous_recording' then observed_internal_awake_bouts end as awakening_count
from picked
