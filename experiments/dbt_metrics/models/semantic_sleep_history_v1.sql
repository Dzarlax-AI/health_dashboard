{{ config(enabled=var('semantic_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

-- One decision for every calendar day in [D-30,D), even when D has no night.
with decisions as (
    select c.date as wake_date, (c.date::date - offset_days)::text as history_date,
           case
             when n.wake_date is null then 'no_current_night'
             when h.wake_date is null then 'no_history_night'
             when h.source <> n.source then 'source_changed'
             when h.source_epoch <> n.source_epoch then 'epoch_changed'
             when h.algorithm_version <> n.algorithm_version then 'algorithm_changed'
             when h.capture_completeness <> 'complete' then 'capture_' || h.capture_completeness
             when h.duration_assessment <> 'plausible' then 'duration_' || h.duration_assessment
             when h.finalization_state <> 'final' then 'not_final'
             when h.claim_eligibility <> 'eligible' then 'ineligible_claim'
             else 'eligible'
           end as reason
    from {{ ref('native_night_calendar_v1') if var('tenant_native_catalog',false) else source('source_data', 'calendar') }} c
    cross join generate_series(1, 30) offsets(offset_days)
    left join {{ source('source_data', 'completed_night_sleep') }} n on n.wake_date = c.date
    left join {{ source('source_data', 'completed_night_sleep') }} h
      on h.wake_date = (c.date::date - offset_days)::text
)
select wake_date, history_date, reason = 'eligible' as eligible, reason
from decisions
