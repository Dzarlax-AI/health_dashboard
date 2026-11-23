{{ config(enabled=var('briefing_components_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with captures as (
    select profile,capture,inputs->'illness_signals' as signals
    from {{ source('briefing_pilot','captured_inputs') }}
    where jsonb_typeof(inputs->'illness_signals')='array'
)
select * from ({{ illness_suspicion_v1('captures') }}) as suspicion
