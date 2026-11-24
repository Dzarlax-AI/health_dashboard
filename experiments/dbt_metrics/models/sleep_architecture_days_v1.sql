{{ config(enabled=var('briefing_components_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view', post_hook="alter view {{ this }} set (security_invoker = true)") }}
{% if var('tenant_native_catalog',false) %}
select current_user::text as profile,j.decision_key||':'||j.input_hash as capture,d.*
from {{ source('source_data','hdbt_decision_inputs') }} j cross join lateral (
    with segments as (
        select r.*,p.ordinal,j.inputs->>'preferred' as preferred
        from jsonb_array_elements(j.inputs->'segments') with ordinality p(item,ordinal)
        cross join lateral jsonb_to_record(p.item) r(day text,source text,metric text,start_ns bigint,end_ns bigint,hours float8)
        where r.day=j.decision_key
    )
    select * from ({{ sleep_architecture_days() }}) result
) d where j.metric='sleep_architecture_day' and j.formula_version=1
{% else %}

select c.profile,c.capture,d.*
from {{ source('briefing_pilot','architecture_captures') }} c cross join lateral (
    with segments as (
        select s.*,c.preferred_source as preferred from {{ source('briefing_pilot','architecture_segments') }} s
        where (s.profile,s.capture)=(c.profile,c.capture)
    )
    select * from ({{ sleep_architecture_days() }}) result
) d
{% endif %}
