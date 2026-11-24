{{ config(enabled=var('semantic_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

-- This is the app's formula with main-night-specific inputs. An archived
-- same-day ReadinessEvidence bundle is required to reproduce a past screen.
with selected as (
    select c.*, n.duration_hours,n.duration_assessment,n.claim_eligibility,
           case when c.awake_minutes is not null then 'recorded'
                when c.continuity_reason='continuous_recording' and c.awakening_count=0
                  and c.deep_minutes is not null and c.rem_minutes is not null and c.core_minutes is not null
                then 'inferred_zero_complete_stages' else 'missing' end as awake_basis
    from {{ ref('semantic_sleep_continuity_v1') }} c
    left join {{ source('source_data','completed_night_sleep') }} n using (wake_date)
), inputs as (
    select c.wake_date,c.awake_basis,c.duration_hours as total_hours,c.deep_minutes::double precision/60 as deep_hours,
           c.rem_minutes::double precision/60 as rem_hours,case when c.awake_basis='inferred_zero_complete_stages' then 0::double precision
                else c.awake_minutes::double precision/60 end as awake_hours,
           case when c.source is null then 'no_canonical_night'
                when c.duration_hours<=0 then 'no_positive_duration'
                when c.capture_completeness<>'complete' then 'incomplete_capture'
                when c.duration_assessment<>'plausible' then 'duration_not_plausible'
                when c.stage_reason not in ('recorded_episode','matched_day_summary') then c.stage_reason
                when c.deep_minutes is null or c.rem_minutes is null or c.awake_basis='missing' then 'missing_stage_components'
                when c.finalization_state<>'final' or c.claim_eligibility<>'eligible' then 'night_not_final_eligible'
                when c.stage_reason='recorded_episode' and c.continuity_reason in ('gaps_in_episode','open_awake_boundary') then c.continuity_reason
                else c.stage_reason end as quality_basis
    from selected c
), attributed as (
    select *,quality_basis in ('recorded_episode','matched_day_summary','gaps_in_episode','open_awake_boundary') as evidence_present,
           case when quality_basis in ('recorded_episode','matched_day_summary','gaps_in_episode','open_awake_boundary') then 'ok' else 'missing' end as evidence_freshness,
           case when quality_basis in ('recorded_episode','matched_day_summary') then 'final'
                when quality_basis in ('gaps_in_episode','open_awake_boundary') then 'low' else 'provisional' end as evidence_confidence
    from inputs
)
select evaluated.*, (quality->>'score_pct')::integer as quality_score_pct
from ({{ semantic_sleep_quality('attributed') }}) evaluated
