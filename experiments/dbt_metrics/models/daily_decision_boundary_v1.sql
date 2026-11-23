{{ config(enabled=var('briefing_components_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with typed as (
    select profile,capture,inputs->'decision' as b from {{ source('briefing_pilot','captured_inputs') }}
), decision_inputs as (
    select profile,capture,(b->>'readiness_today')::int as readiness,
           case when nullif(b->'today_guidance','null'::jsonb) is not null then coalesce(b#>>'{today_guidance,action}','') end as guidance_action,
           case when nullif(b->'energy_bank','null'::jsonb) is not null then coalesce(b#>>'{energy_bank,action_verdict}','') end as bank_action,
           b#>>'{illness_suspicion,confidence}' as illness, b#>>'{sleep_quality,confidence}' as sleep
    from typed
)
{{ daily_decision_boundary() }}
