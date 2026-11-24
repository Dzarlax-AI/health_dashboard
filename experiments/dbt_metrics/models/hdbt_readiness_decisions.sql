{{ config(enabled=var('hdbt_decision_worker', false), materialized='table', alias='hdbt_w_readiness') }}
with receipts as (
    select j.decision_key, j.input_hash, j.formula_version, j.inputs
    from {{ adapter.quote(target.schema) }}.hdbt_decision_inputs j
    join {{ adapter.quote(target.schema) }}.hdbt_i_decisions using(metric,decision_key,input_hash)
    where j.metric='readiness' and j.formula_version=3
), inputs as (
    select j.input_hash as case_id, current_user::text as profile,
           i."Date" as target_date, j.inputs->>'reader' as reader,
           i."HRV" as hrv, i."RHR" as rhr, i."Sleep" as sleep,
           i."HRVPresent" as hrv_present, i."RHRPresent" as rhr_present, i."SleepPresent" as sleep_present,
           i."LowQuality" as low_sleep_quality, i."Provisional" as hrv_provisional,
           i."Samples" as hrv_samples, i."Illness" as illness, ''::text as headline, true as captured
    from receipts j
    cross join lateral jsonb_to_record(j.inputs->'effective') as i(
        "Date" text, "HRV" float8[], "RHR" float8[], "Sleep" float8[],
        "HRVPresent" boolean, "RHRPresent" boolean, "SleepPresent" boolean,
        "LowQuality" boolean, "Provisional" boolean, "Samples" int, "Illness" text)
    where j.decision_key=(j.inputs->>'reader')||'/'||i."Date"
), eligibility as (
    {{ readiness_effective_eligibility_v1('inputs') }}
), hrv_scores as (
    {{ readiness_hrv_math_v1('eligibility') }}
), captures as (
    select *, weighted_z as hrv_weighted_z, component_weight as hrv_weight, skip_reason as hrv_reason
    from hrv_scores
), scores as (
    {{ readiness_score_v1('captures') }}
)
select 'readiness'::text as metric, reader||'/'||target_date as decision_key,
       case_id as input_hash, 3 as formula_version,
       jsonb_build_object('RawScore',raw_score,'DisplayScore',display_score,
                          'Confidence',coalesce(confidence,''),'CapReason',coalesce(cap_reason,'')) as result
from scores
