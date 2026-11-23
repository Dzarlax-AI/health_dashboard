{% macro energy_guidance_v1(bands) %}
with basic as (
  select *,case when hrv_z<=-1 or display_bank<=trunc(thresholds[1]) then 'rest'
              when hrv_z<=-0.5 or display_bank<=trunc(thresholds[2]) then 'active_recovery'
              when hrv_z>=0.5 and display_bank>=trunc(thresholds[3]) then 'push_hard' else 'moderate' end as basic_verdict
  from {{ bands }}
), stress as (
  select *,case when 'illness_signature'=any(flags) then 'rest'
               when 'recovery_debt'=any(flags) and basic_verdict in ('moderate','push_hard') then 'active_recovery'
               else basic_verdict end as stress_verdict,
           case when 'illness_signature'=any(flags) then 'illness_signature'
               when 'recovery_debt'=any(flags) and basic_verdict in ('moderate','push_hard') then 'recovery_debt'
               when basic_verdict='push_hard' then 'full_capacity'
               when basic_verdict='moderate' then 'optimal'
               when basic_verdict='rest' and hrv_z<=-1 then 'high_stress' else 'low_capacity' end as stress_reason
  from basic
), safety as (
  select *,case when confidence='high' and stress_verdict<>'rest' then 'illness_suspicion_high'
               when confidence='moderate' and stress_verdict in ('push_hard','moderate') then
                 case when pattern='autonomic_prodrome' then 'autonomic_prodrome_moderate' else 'illness_suspicion_moderate' end
               end as cap_reason
  from stress
)
select profile,capture_id,mode,latest_days,compatible_days,used_days,trunc(thresholds[1])::int as rest,
       trunc(thresholds[2])::int as recovery,trunc(thresholds[3])::int as push,
       case when cap_reason='illness_suspicion_high' then 'rest' when cap_reason is not null then 'active_recovery'
            else stress_verdict end as verdict,coalesce(cap_reason,stress_reason) as reason,
       cap_reason is null and stress_reason not in ('illness_signature','recovery_debt')
            and 'parasympathetic_rebound'=any(flags) as rebound
from safety
{% endmacro %}
