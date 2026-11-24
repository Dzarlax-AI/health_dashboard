{{ config(enabled=var('briefing_components_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view', post_hook="alter view {{ this }} set (security_invoker = true)") }}
{% if var('tenant_native_catalog',false) %}
select current_user::text as profile,j.decision_key||':'||j.input_hash as capture,w.*
from {{ source('source_data','hdbt_decision_inputs') }} j cross join lateral (
    with anchors as (select j.decision_key::date as anchor),
    days as (
        select v.key as day,v.value as hash from jsonb_each_text(j.inputs->'days') v
        join {{ source('source_data','hdbt_decision_inputs') }} receipt
          on receipt.metric='sleep_architecture_day' and receipt.formula_version=1
           and (receipt.decision_key,receipt.input_hash)=(v.key,v.value)
        where v.key between (j.decision_key::date-13)::text and j.decision_key
    ), architecture as (
        select d.* from days v join {{ ref('sleep_architecture_days_v1') }} d
        on d.profile=current_user and d.capture=v.day||':'||v.hash and d.day=v.day
    )
    select * from ({{ sleep_architecture_windows() }}) result where (select count(*) from days)=14
) w where j.metric='sleep_architecture_window' and j.formula_version=1
{% else %}

select c.profile,c.capture,w.*
from {{ source('briefing_pilot','architecture_captures') }} c cross join lateral (
    with anchors as (select c.from_day+n as anchor from generate_series(0,c.to_day-c.from_day) n),
    architecture as (
        select * from {{ ref('sleep_architecture_days_v1') }} d where (d.profile,d.capture)=(c.profile,c.capture)
    )
    select * from ({{ sleep_architecture_windows() }}) result
) w
{% endif %}
