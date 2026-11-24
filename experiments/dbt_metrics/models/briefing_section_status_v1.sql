{{ config(enabled=var('tenant_native_catalog', false) or var('briefing_components_shadow', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select profile, capture, sections->>'activity' as activity, sections->>'cardio' as cardio
from {{ ref('briefing_components_v1') }}
