{{ config(enabled=var('semantic_shadow', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

-- One row per evaluated wake date in the isolated profile.
select d.wake_date as night_id, d.wake_date::date as wake_day,
       d.duration_minutes, d.reference_minutes, d.delta_minutes,
       c.deep_minutes, c.rem_minutes, c.core_minutes, c.unspecified_minutes, c.awake_minutes,
       c.awakening_count, q.quality_score_pct
from {{ ref('semantic_sleep_day_v1') }} d
join {{ ref('semantic_sleep_continuity_v1') }} c using (wake_date)
join {{ ref('semantic_sleep_quality_v1') }} q using (wake_date)
