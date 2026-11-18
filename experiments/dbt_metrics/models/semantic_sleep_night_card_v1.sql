{{ config(enabled=var('semantic_shadow', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select c.wake_date,jsonb_build_object(
    'wake_date',c.wake_date,'version','semantic_sleep_night_card_v1','source',d.source,'source_epoch',d.source_epoch,
    'capture',d.capture_completeness,'assessment',d.duration_assessment,'finalization',d.finalization_state,
    'duration', jsonb_build_object(
        'status',d.status,'duration_minutes',d.duration_minutes,'reference_minutes',d.reference_minutes,
        'delta_minutes',d.delta_minutes,'eligible_nights',d.eligible_nights,'window_start',d.window_start,
        'window_end_exclusive',d.window_end_exclusive),
    'stages', jsonb_build_object(
        'deep_minutes',c.deep_minutes,'rem_minutes',c.rem_minutes,'core_minutes',c.core_minutes,
        'unspecified_minutes',c.unspecified_minutes,'awake_minutes',c.awake_minutes,
        'recorded_asleep_minutes',c.recorded_asleep_minutes,'deep_fraction',c.deep_fraction,'rem_fraction',c.rem_fraction,
        'core_fraction',c.core_fraction,'unspecified_fraction',c.unspecified_fraction,'stage_basis',c.stage_basis,
        'stage_reason',c.stage_reason),
    'continuity', jsonb_build_object(
        'awakening_count',c.awakening_count,'observed_internal_awake_bouts',c.observed_internal_awake_bouts,
        'awake_minutes',c.awake_minutes,'continuity_reason',c.continuity_reason,
        'gap_minutes',c.gap_minutes,'episode_start',to_char(c.episode_start at time zone 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
        'episode_end',to_char(c.episode_end at time zone 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"')),
    'quality',q.quality,'quality_basis',q.quality_basis,'quality_awake_basis',q.awake_basis,
    'input_version',encode(sha256(convert_to(jsonb_build_array(d.input_version,c.input_version)::text,'UTF8')),'hex')
) as card
from {{ ref('semantic_sleep_continuity_v1') }} c
join {{ ref('semantic_sleep_day_v1') }} d using (wake_date)
join {{ ref('semantic_sleep_quality_v1') }} q using (wake_date)
