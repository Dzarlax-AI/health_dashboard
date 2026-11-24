{{ config(enabled=var('readiness_chips_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view') }}

{% if var('tenant_native_catalog', false) %}
with selected as (
    select j.decision_key||':'||j.input_hash as case_id,i.*
    from {{ source('source_data','hdbt_decision_inputs') }} j
    cross join lateral jsonb_to_record(j.inputs) i(epoch text,as_of_date date,pairs jsonb)
    where j.metric='chip_calibration' and j.formula_version=1 and jsonb_typeof(j.inputs->'pairs')='array'
), pairs as (
    select i.case_id,p.* from selected i cross join lateral jsonb_to_recordset(i.pairs) p(
        day date,prediction_epoch text,label_epoch text,baseline_kind text,predicted float8,label float8,eligible bool)
)
select * from ({{ chip_calibration_v1('selected','pairs') }}) computed
{% else %}
{{ chip_calibration_v1(source('chip_captures','chip_calibration_input'), source('chip_captures','chip_pairs')) }}
{% endif %}
