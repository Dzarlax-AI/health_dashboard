{{ config(enabled=var('briefing_components_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with typed as (
    select profile,{% if var('decision_journal_replay', false) %}to_jsonb(capture){% else %}capture{% endif %} as capture,inputs->'decision' as b from {{ source('briefing_pilot','captured_inputs') }}
), decision_inputs as (
    select profile,capture,(b->>'readiness_today')::int as readiness,
           case when nullif(b->'today_guidance','null'::jsonb) is not null then coalesce(b#>>'{today_guidance,action}','') end as guidance_action,
           case when nullif(b->'energy_bank','null'::jsonb) is not null then coalesce(b#>>'{energy_bank,action_verdict}','') end as bank_action,
           b#>>'{illness_suspicion,confidence}' as illness, b#>>'{sleep_quality,confidence}' as sleep
    from typed
    {% if var('decision_journal_replay', false) %}
    union all
    select current_user::text,to_jsonb('journal:'||input_hash),(inputs->>'readiness')::int,
           inputs->>'guidance_action',inputs->>'bank_action',inputs->>'illness',inputs->>'sleep'
    from {{ source('source_data','hdbt_decision_inputs') }}
    where metric='today_decision' and formula_version=1
    {% endif %}
)
, evaluated as ({{ daily_decision_boundary() }})
, headlines as (
    select profile,capture,b#>>'{headline,key}' as headline from typed
    {% if var('decision_journal_replay', false) %}
    union all
    select current_user::text,to_jsonb('journal:'||input_hash),inputs->>'headline'
    from {{ source('source_data','hdbt_decision_inputs') }}
    where metric='today_decision' and formula_version=1
    {% endif %}
)
select e.*,array[e.decision_domain] as evidence_domains,
       case when nullif(t.headline,'') is not null then array[t.headline] end as signal_keys
from evaluated e join headlines t using(profile,capture)
