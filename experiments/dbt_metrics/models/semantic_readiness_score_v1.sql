{% set native = var('tenant_native_catalog', false) %}
{{ config(enabled=native or var('readiness_score_shadow', false), materialized='ephemeral' if native else 'view', post_hook="alter view {{ this }} set (security_invoker = true)") }}
with {% if not native %}captures as (
    select s.*, h.weighted_z as hrv_weighted_z, h.component_weight as hrv_weight,
           h.evidence_present as hrv_present, h.skip_reason as hrv_reason,
           h.snapshot_state='captured' and h.skip_reason is distinct from 'invalid_snapshot' as captured
    from {{ source('readiness_captures','readiness_score_inputs') }} s
    left join {{ ref('semantic_readiness_hrv_component_v1') }} h
      on h.profile=s.profile and h.snapshot_id=s.hrv_snapshot_id
      and h.target_date=s.target_date and h.reader=s.reader
)
, {% endif %}all_scores as (
{% if not native %}
select score.*, score.reader <> 'morning' or exists (
    select from {{ source('readiness_captures','readiness_score_inputs') }} c
    where c.profile=score.profile and c.case_id=score.case_id and c.headline is not null
) as has_final_context from ({{ readiness_score_v1('captures') }}) as score

{% endif %}
{% if native or var('decision_journal_replay', false) %}
{% if not native %}union all{% endif %}
select journal_replay.*, reader <> 'morning' or case_id like 'briefing:%' as has_final_context from (
    with inputs as (
        select (case j.metric when 'today_briefing' then 'briefing:' else 'journal:' end||j.input_hash)::text as case_id,current_user::text as profile,
               i."Date"::date as target_date, j.inputs->>'reader' as reader,
               i."HRV" as hrv, i."RHR" as rhr, i."Sleep" as sleep,
               i."HRVPresent" as hrv_present, i."RHRPresent" as rhr_present, i."SleepPresent" as sleep_present,
               i."LowQuality" as low_sleep_quality, i."Provisional" as hrv_provisional,
               i."Samples" as hrv_samples, case when 'high' in (i."Illness",j.inputs->>'final_illness') then 'high'
                    when 'moderate' in (i."Illness",j.inputs->>'final_illness') then 'moderate'
                    else i."Illness" end as illness, j.inputs->>'headline' as headline, true as captured
        from {{ source('source_data','hdbt_decision_inputs') }} j
        cross join lateral jsonb_to_record(j.inputs->'effective') as i(
              "Date" text, "HRV" float8[], "RHR" float8[], "Sleep" float8[],
              "HRVPresent" boolean, "RHRPresent" boolean, "SleepPresent" boolean,
              "LowQuality" boolean, "Provisional" boolean, "Samples" int, "Illness" text)
        where (j.metric='readiness' or (j.metric='today_briefing' and jsonb_typeof(j.inputs->'headline')='string'
              and jsonb_typeof(j.inputs->'final_illness')='string')) and j.formula_version=3
        {% if native %}and j.metric='today_briefing' and j.inputs->>'reader'='morning'
          and j.inputs->'effective' ?& array['Date','HRV','RHR','Sleep','HRVPresent','RHRPresent','SleepPresent',
                                            'LowQuality','Provisional','Samples','Illness']
          and (j.inputs#>>'{effective,Date}')=j.decision_key {% endif %}
    ), eligibility as (
        {{ readiness_effective_eligibility_v1('inputs') }}
    ), hrv_scores as (
        {{ readiness_hrv_math_v1('eligibility') }}
    ), retained as (
        select *,weighted_z as hrv_weighted_z, component_weight as hrv_weight, skip_reason as hrv_reason from hrv_scores
    )
    select * from ({{ readiness_score_v1('retained') }}) evaluated
) journal_replay
{% endif %}

)
select case_id, profile, target_date, reader, status, hrv_contribution, rhr_contribution,
       sleep_contribution, hrv_skip_reason, rhr_skip_reason, sleep_skip_reason, weighted_sum,
       available_weight, raw_score,
       case when has_final_context then display_score end as display_score,
       case when has_final_context then band end as band,
       case when has_final_context then confidence end as confidence,
       case when has_final_context then cap_reason end as cap_reason,
       case when has_final_context and display_score is not null then 'readiness_legacy_alias' end as recovery_source,
       display_score as solver_display_score, confidence as solver_confidence, cap_reason as solver_cap_reason
from all_scores
