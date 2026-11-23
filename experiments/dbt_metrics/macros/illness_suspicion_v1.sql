{% macro illness_suspicion_v1(inputs) %}
with evidence as (
    select i.profile,i.capture,count(distinct s.category) filter(where s.objective) as categories,
           bool_or(s.objective and s.strong) as strong_objective,
           bool_or(s.objective and s.primary_signal and s.category='respiratory') as respiratory,
           bool_or(s.objective and s.primary_signal and s.category='respiratory' and s.strong) as strong_respiratory,
           bool_or(s.objective and s.category='oxygen') as oxygen,
           bool_or(s.objective and s.auto_or_temp) as autonomic_or_temperature,
           bool_or(s.confounder) as confounder,bool_or(s.prodrome) as prodrome,
           bool_or(s.persistence) as persistence,bool_or(s.subjective_sick) as sick,
           bool_or(s.illness_signature) as signature
    from {{ inputs }} i
    left join lateral jsonb_to_recordset(i.signals) as s(category text,objective bool,primary_signal bool,
        strong bool,auto_or_temp bool,confounder bool,prodrome bool,persistence bool,subjective_sick bool,
        illness_signature bool) on true
    group by i.profile,i.capture
), classified as (
    select *,case when signature then 'high'
                  when categories>=3 and strong_respiratory and autonomic_or_temperature and not confounder then 'high'
                  when respiratory and (oxygen or autonomic_or_temperature) then 'moderate'
                  when persistence and categories>0 then 'moderate'
                  when prodrome or (strong_objective and sick) then 'moderate'
                  when categories>0 then 'low' else 'none' end as confidence
    from evidence
)
select profile,capture,confidence,
       case when confidence='none' then ''
            when prodrome and not respiratory and not oxygen then 'autonomic_prodrome'
            when respiratory and (oxygen or prodrome) then 'mixed'
            when respiratory then 'respiratory' when oxygen then 'oxygen' else '' end as pattern
from classified
{% endmacro %}
