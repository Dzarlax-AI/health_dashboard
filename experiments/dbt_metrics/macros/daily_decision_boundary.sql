{% macro daily_decision_boundary() %}
select profile,capture,
       case when guidance_action is not null then guidance_action
            when bank_action is not null then bank_action
            when readiness >= 80 then 'moderate'
            when readiness >= 50 then 'active_recovery'
            else 'rest' end as decision_mode,
       case when guidance_action is not null then
                 case when coalesce(illness,'') not in ('high','moderate')
                        and coalesce(sleep,'') <> 'final'
                      then 'sleep' else 'recovery' end
            when bank_action is not null then 'energy' else 'recovery' end as decision_domain
from decision_inputs
{% endmacro %}
