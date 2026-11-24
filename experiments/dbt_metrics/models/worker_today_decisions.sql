{{ config(enabled=var('hdbt_decision_worker', false), alias='hdbt_w_today', materialized='table') }}

with receipts as (
    select j.decision_key, j.input_hash, j.formula_version, j.inputs
    from {{ adapter.quote(target.schema) }}.hdbt_decision_inputs j
    join {{ adapter.quote(target.schema) }}.hdbt_i_decisions using(metric,decision_key,input_hash)
    where j.metric='today_decision' and j.formula_version=1
), decision_inputs as (
    select decision_key as profile, input_hash as capture, (inputs->>'readiness')::int as readiness,
           inputs->>'guidance_action' as guidance_action, inputs->>'bank_action' as bank_action,
           inputs->>'illness' as illness, inputs->>'sleep' as sleep
    from receipts
), evaluated as ({{ daily_decision_boundary() }})
select 'today_decision'::text as metric, r.decision_key, r.input_hash, r.formula_version,
       jsonb_build_object('mode',e.decision_mode,'evidence_domains',array[e.decision_domain],
           'signal_keys',case when nullif(r.inputs->>'headline','') is not null then array[r.inputs->>'headline'] end) as result
from evaluated e join receipts r on (r.decision_key,r.input_hash)=(e.profile,e.capture)
