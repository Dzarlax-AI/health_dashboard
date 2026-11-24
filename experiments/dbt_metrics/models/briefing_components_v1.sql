{{ config(enabled=var('tenant_native_catalog', false) or var('briefing_components_shadow', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with captures as (
    {% if var('tenant_native_catalog', false) %}
    select profile,capture,jsonb_build_object('raw',inputs->'section_inputs_v1') as inputs
    from ({{ today_metric_captures_v1() }}) c
    where inputs->'section_inputs_v1' ?& array['Steps','Cal','Exercise','SpO2','VO2','Resp']
    {% else %}select profile, capture, inputs from {{ source('briefing_pilot','captured_inputs') }}{% endif %}
), scored as ({{ briefing_sections('captures') }})
select * from scored
