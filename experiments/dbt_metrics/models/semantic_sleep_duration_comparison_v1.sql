{{ config(enabled=var('semantic_shadow', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

-- One row per canonical wake date. The reader maps an absent row to no_night.
with inputs as (
    select n.wake_date, n.duration_hours, n.source, n.source_epoch, n.algorithm_version,
           n.input_hash, n.capture_completeness, n.duration_assessment, n.finalization_state,
           n.claim_eligibility, n.observed_at, r.window_start, r.window_end_exclusive,
           r.eligible_nights, r.reference_hours, r.history_version, r.rule_version
    from {{ source('source_data','completed_night_sleep') }} n
    join {{ ref('sleep_duration_reference_v1') }} r
      using (wake_date, source, source_epoch, algorithm_version)
)
select wake_date, status, duration_minutes, reference_minutes,
       delta_minutes, eligible_nights,
       window_start, window_end_exclusive, observed_at, source, source_epoch,
       algorithm_version, input_hash, capture_completeness, duration_assessment,
       finalization_state, claim_eligibility, history_version, rule_version
from inputs
{{ sleep_duration_comparison_fields() }}
