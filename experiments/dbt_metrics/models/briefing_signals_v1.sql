{{ config(enabled=var('briefing_components_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with captures as (select * from {{ source('briefing_pilot','captured_inputs') }}),
steps as (
 select profile,capture,s,position,greatest(1,max((s->>'Val')::float8) over(partition by profile,capture)) as max_steps
 from captures cross join lateral jsonb_array_elements(coalesce(nullif(inputs->'raw'->'StepsWithDates','null'),'[]')) with ordinality t(s,position)
), paired as (
 select c.profile,c.capture,jsonb_agg(jsonb_build_object('date',s->>'Date','load',
    round(((s->>'Val')::float8/max_steps*100)::numeric,1),'hrv',round((h->>'Val')::numeric,1)) order by position) as series
 from captures c join steps using(profile,capture)
 join lateral (select h from jsonb_array_elements(coalesce(nullif(inputs->'raw'->'HRVWithDates','null'),'[]')) with ordinality t(h,i)
               where h->>'Date'=s->>'Date' order by i desc limit 1) matched on true
 group by 1,2
)
select c.profile,c.capture,signals.*,
  case when inputs->>'walking' is null then 'no_walking_hr' when (inputs->>'walking')::float8 between 50 and 180 then 'ok'
       else 'walking_hr_out_of_range' end as walking_reason, coalesce(paired.series,'[]') as correlation
from captures c
cross join lateral (select
    array(select z::float8 from jsonb_array_elements_text(coalesce(nullif(inputs->'hour_z','null'), '[]'))
          with ordinality as h(z, position) order by position) as hour_z,
    coalesce((inputs->>'z_threshold')::float8,1) as threshold,
    (inputs->>'temp')::float8 as temp, (inputs->>'resp')::float8 as resp,
    (inputs->>'drop')::float8 as hrv_drop, (inputs->>'rhr')::float8 as rhr,
    (inputs->>'day_hr')::float8 as day_hr) i
cross join lateral ({{ briefing_stress_signals() }}) signals
left join paired using(profile,capture)
