{{ config(enabled=var('readiness_chips_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view') }}

{% if var('tenant_native_catalog', false) %}
with selected as (
    select j.decision_key||':'||j.input_hash as case_id,i.*
    from {{ source('source_data','hdbt_decision_inputs') }} j
    cross join lateral jsonb_to_record(j.inputs) i(sub_score text,target_kind text,as_of_date date,epoch_start date,days jsonb)
    where j.metric='chip_baseline' and j.formula_version=1 and jsonb_typeof(j.inputs->'days')='array'
)
select * from ({{ chip_baseline_v1('selected') }}) computed
{% else %}
{{ chip_baseline_v1(source('chip_captures','chip_baseline_input')) }}
{% endif %}
