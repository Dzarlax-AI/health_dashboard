{{ config(enabled=var('readiness_chips_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view') }}

{% if var('tenant_native_catalog', false) %}
with selected as (
    select j.decision_key||':'||j.input_hash as case_id,i.*
    from {{ source('source_data','hdbt_decision_inputs') }} j
    cross join lateral jsonb_to_record(j.inputs) i(sub_score text,target_kind text,target_date date,latest_sleep date,paired_count int,breach_threshold int,days jsonb)
    where j.metric='chip_targets' and j.formula_version=1 and jsonb_typeof(j.inputs->'days')='array'
)
select * from ({{ chip_targets_v1('selected') }}) computed
{% else %}
{{ chip_targets_v1(source('chip_captures','chip_target_input')) }}
{% endif %}
