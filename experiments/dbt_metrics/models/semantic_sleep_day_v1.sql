{{ config(enabled=var('semantic_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

with interval_summary as (
    select i.wake_date, count(*) as interval_count,
           count(*) filter (where i.stage in ('deep','rem','core')) as detailed_intervals,
           bool_or(exists (
               select from {{ ref('semantic_sleep_intervals_v1') }} other
               where other.wake_date = i.wake_date and other.stage <> i.stage
                 and other.start_at < i.end_at - interval '1 second'
                 and i.start_at < other.end_at - interval '1 second'
           )) as overlap,
           encode(sha256(convert_to(jsonb_agg(jsonb_build_array(i.stage, extract(epoch from i.start_at),
               extract(epoch from i.end_at)) order by i.start_at, i.end_at, i.stage)::text,'UTF8')),'hex') as interval_version
    from {{ ref('semantic_sleep_intervals_v1') }} i group by i.wake_date
), stage_day as (
    select s.date, s.source,
           array_agg(s.metric_name order by s.metric_name) as stage_keys,
           count(*) filter (where s.metric_name in ('sleep_deep','sleep_rem','sleep_core')) as detailed_keys
    from {{ ref('native_sleep_stage_day_v1') if var('tenant_native_catalog',false) else source('source_data', 'sleep_stage_day') }} s
    group by s.date, s.source
), inputs as (
    select c.date as wake_date, n.source, n.source_epoch, n.algorithm_version, n.input_hash,
           n.capture_completeness, n.duration_assessment, n.finalization_state, n.claim_eligibility,
           n.duration_hours, r.eligible_nights, r.reference_hours, r.history_version, r.eligible_dates,
           (c.date::date - 30)::text as window_start, c.date as window_end_exclusive,
           coalesce(i.interval_count, 0) as interval_count, coalesce(i.overlap, false) as conflicting_stages,
           coalesce(s.detailed_keys,0) as day_stage_key_count,
           case when n.wake_date is null then 'no_night'
                when i.overlap then 'overlapping_stage_labels'
                when i.detailed_intervals > 0 then 'recorded_episode_stages'
                when i.interval_count > 0 then 'episode_without_detailed_stages'
                when s.detailed_keys > 0 then 'day_summary_only'
                else 'no_detailed_stages' end as stage_status,
           encode(sha256(convert_to(jsonb_build_array(c.date, n.input_hash, n.source, n.source_epoch, n.algorithm_version,
               n.duration_hours, n.capture_completeness, n.duration_assessment, n.finalization_state,
               n.claim_eligibility, r.history_version, i.interval_version, s.stage_keys)::text,'UTF8')),'hex') as input_version
    from {{ ref('native_night_calendar_v1') if var('tenant_native_catalog',false) else source('source_data','calendar') }} c
    left join {{ source('source_data','completed_night_sleep') }} n on n.wake_date = c.date
    left join {{ adapter.quote(target.schema) ~ '.sleep_duration_reference_v1' if var('tenant_native_catalog',false) else ref('sleep_duration_reference_v1') }} r on r.wake_date = c.date
    left join interval_summary i on i.wake_date = c.date
    left join stage_day s on s.date = c.date and s.source = n.source
)
select inputs.*, status, duration_minutes, reference_minutes,
       delta_minutes
from inputs
{{ sleep_duration_comparison_fields() }}
