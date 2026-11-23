{{ config(enabled=var('energy_bank_shadow', false), materialized='view',
          post_hook='alter view {{ this }} set (security_invoker=true)') }}

{% if var('decision_journal_replay', false) %}
with journal as (
    select 'journal:'||input_hash as capture_id,inputs from {{ source('source_data','hdbt_decision_inputs') }}
    where metric='energy_bank' and formula_version=3
), runs as (
    select * from {{ source('energy_capture','runs') }}
    union all
    select current_user::text,j.capture_id,r.target_day,r.snapshot_kind,r.formula_version,
           r.alpha,r.factor,r.stress_enabled,r.beta
    from journal j cross join lateral jsonb_to_record(j.inputs->'run') as r(
        target_day date,snapshot_kind text,formula_version int,alpha float8,factor float8,stress_enabled bool,beta float8)
), days as (
    select * from {{ source('energy_capture','days') }}
    union all
    select current_user::text,j.capture_id,d.day,d.sleep_total,d.deep,d.rem,d.awake,d.active_kcal,d.hr_load,d.stress_flags
    from journal j cross join lateral jsonb_to_recordset(j.inputs->'days') as d(
        day date,sleep_total float8,deep float8,rem float8,awake float8,active_kcal float8,hr_load float8,stress_flags text[])
)
select * from ({{ energy_bank_v3('runs', 'days') }}) as replay
{% else %}
{{ energy_bank_v3(source('energy_capture','runs'), source('energy_capture','days')) }}
{% endif %}
