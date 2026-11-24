{% macro native_published_decisions(model) %}
with receipts as (
    select p.decision_key, p.input_hash, p.result, j.inputs
    from {{ adapter.quote(target.schema) }}.hdbt_decision_results p
    join {{ adapter.quote(target.schema) }}.hdbt_decision_inputs j
        using (metric,decision_key,input_hash,formula_version)
    where exists(select from {{ adapter.quote(target.schema) }}.hdbt_state s
                 where s.singleton and s.shadow_enabled)
      and exists(select from {{ adapter.quote(target.schema) }}.settings
                 where key='dbt_decision_result_reads_v1' and value='true')
      {% if model == 'semantic_readiness_score_v1' %}
      and p.metric='readiness' and p.formula_version=3
      {% elif model == 'semantic_energy_bank_v3' %}
      and p.metric='energy_bank' and p.formula_version=3
      {% elif model == 'daily_decision_boundary_v1' %}
      and p.metric='today_decision' and p.formula_version=1
      {% else %}
      {{ exceptions.raise_compiler_error("unknown native decision producer") }}
      {% endif %}
)
{% if model == 'semantic_readiness_score_v1' %}
select current_user::text as profile,'journal:'||input_hash as case_id,inputs#>>'{effective,Date}' as target_date,
       inputs->>'reader' as reader, (result->>'RawScore')::int as raw_score,
       null::int as display_score, case when inputs->>'reader'='history' then
         case when (result->>'DisplayScore')::int>=80 then 'optimal' when (result->>'DisplayScore')::int>=50 then 'fair' else 'low' end
       end as band,null::text as confidence,null::text as cap_reason,null::text as recovery_source,
       inputs->>'reader'='history' as final_context
from receipts
union all
select profile,case_id,target_date::text,reader,raw_score,display_score,band,confidence,cap_reason,recovery_source,true
from {{ ref('semantic_readiness_score_v1') }}
{% elif model == 'semantic_energy_bank_v3' %}
select current_user::text as profile,'journal:'||input_hash as capture_id,
       result->'Bank' as signed_bank,result->'Display' as display_bank,result->'State' as state,
       result->'Flags' as flags,result->'FormulaVersion' as formula_version,
       result->'AlphaUsed' as alpha_used,result->'TodayDrain' as drain_delta,result->'TodayRestore' as restore_delta,
       result->'Components' as components
from receipts
{% else %}
select current_user::text as profile,to_jsonb('journal:'||input_hash) as capture,
       result->'mode' as decision_mode,result->'evidence_domains' as evidence_domains,
       result->'signal_keys' as signal_keys
from receipts
{% endif %}
{% endmacro %}
