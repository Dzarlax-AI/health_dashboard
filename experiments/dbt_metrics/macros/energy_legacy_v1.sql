{% macro energy_legacy_v1(inputs) %}
with inputs as (
  select i.*, coalesce(r.mean,0) as rhr_base, case when coalesce(cardinality(hrv),0)<9 or coalesce(cardinality(sleep),0)<9 or readiness<=0 or sleep[1]<=0 then 'unavailable'
                   when coalesce(cardinality(rhr),0)=0 then 'unavailable' else 'available' end as state,
         case when h.sd>=1e-9 then (hrv[1]-h.mean)/h.sd else 0 end as hrv_z,
         case when r.sd>=1e-9 then (rhr[1]-r.mean)/r.sd else 0 end as rhr_z
  from {{ inputs }} i
  cross join lateral (select avg(x) as mean,stddev_pop(x) as sd from unnest(i.hrv[8:]) x) h
  cross join lateral (select avg(x) as mean,stddev_pop(x) as sd from unnest(i.rhr[8:]) x) r
), loads as (
  select *,floor(0.5+100*least(1,greatest(0,
           0.5*case when usual_steps>1 then steps/usual_steps else 0 end+
           0.5*case when usual_kcal>1 then kcal/usual_kcal else 0 end)))::int as strain,
         floor(0.5+100*least(1,(greatest(0,rhr_z)+greatest(0,-hrv_z))/2))::int as stress
  from inputs
), drain as (
  select *,strain*(1+0.5::float8*stress/100) as drain from loads
), balances as (
  select *,greatest(0,least(100,floor(readiness-drain+0.5)))::int as current,
          greatest(0,least(100,floor(drain+0.5)))::int as drained
  from drain
), verdicts as (
  select *,case when hrv_z<=-1 or current<=25 then 'rest'
                when hrv_z<=-0.5 or current<=45 then 'active_recovery'
                when hrv_z>=0.5 and current>=60 then
                  case when headline='stress' then 'active_recovery' else 'push_hard' end
                else 'moderate' end as verdict
  from balances
)
select profile,capture_id,state,rhr_base,
       case when state='available' then readiness end as capacity,
       case when state='available' then current end as current,
       case when state='available' then drained end as drained,
       case when state='available' then strain end as strain,
       case when state='available' then stress end as stress,
       case when state='available' then hrv_z end as hrv_z,
       case when state='available' then verdict end as verdict,
       case when state='available' then
          case when verdict='push_hard' then 'full_capacity'
               when (verdict='rest' and hrv_z<=-1) or (verdict='active_recovery' and stress>=50) then 'high_stress'
               when verdict='active_recovery' and strain>=80 then 'acwr_spike'
               when verdict in ('rest','active_recovery') then 'low_capacity' else 'optimal' end end as reason
from verdicts
{% endmacro %}
