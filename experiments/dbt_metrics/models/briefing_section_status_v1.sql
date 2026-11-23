{{ config(enabled=var('briefing_components_shadow', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select profile, capture, sections->>'activity' as activity, sections->>'cardio' as cardio
from {{ ref('briefing_components_v1') }}
