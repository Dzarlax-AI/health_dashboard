{{ config(enabled=var('readiness_score_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}
with captures as (
    select s.*, h.weighted_z as hrv_weighted_z, h.component_weight as hrv_weight,
           h.evidence_present as hrv_present, h.skip_reason as hrv_reason,
           h.snapshot_state='captured' and h.skip_reason is distinct from 'invalid_snapshot' as captured
    from {{ source('readiness_captures','readiness_score_inputs') }} s
    left join {{ ref('semantic_readiness_hrv_component_v1') }} h
      on h.profile=s.profile and h.snapshot_id=s.hrv_snapshot_id
      and h.target_date=s.target_date and h.reader=s.reader
)
select * from ({{ readiness_score_v1('captures') }}) as score
