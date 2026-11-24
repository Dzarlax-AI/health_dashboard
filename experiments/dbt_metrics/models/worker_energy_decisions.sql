{{ config(enabled=var('hdbt_decision_worker', false), alias='hdbt_w_energy', materialized='table') }}

with journal as (
    select metric, decision_key, input_hash, formula_version, inputs
    from {{ adapter.quote(target.schema) }}.hdbt_decision_inputs
    join {{ adapter.quote(target.schema) }}.hdbt_i_decisions using(metric,decision_key,input_hash)
    where metric='energy_bank' and formula_version=3
), runs as (
    select current_user::text as profile, j.input_hash as capture_id, r.*
    from journal j cross join lateral jsonb_to_record(j.inputs->'run') as r(
        target_day date, snapshot_kind text, formula_version int, alpha float8,
        factor float8, stress_enabled bool, beta float8)
), days as (
    select current_user::text as profile, j.input_hash as capture_id, d.*
    from journal j cross join lateral jsonb_to_recordset(j.inputs->'days') as d(
        day date, sleep_total float8, deep float8, rem float8, awake float8,
        active_kcal float8, hr_load float8, stress_flags text[])
), solved as ({{ energy_bank_v3('runs','days') }})
select j.metric,j.decision_key,j.input_hash,j.formula_version,
       jsonb_build_object('Bank',s.signed_bank,'Display',s.display_bank,'State',s.state,
          'Flags',s.flags,'FormulaVersion',s.formula_version,'AlphaUsed',s.alpha_used,
          'TodayDrain',s.drain_delta,'TodayRestore',s.restore_delta,'Components',s.components,
          'Trace',jsonb_build_array(s.unrounded_bank,s.effective_quality,s.unrounded_restore,s.incoming,s.before_clamp)) as result
from journal j join solved s on s.capture_id=j.input_hash and s.target_day::text=j.decision_key
