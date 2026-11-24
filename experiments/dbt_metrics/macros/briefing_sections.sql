{% macro briefing_sections(captures) %}
with series as (
    select c.profile,c.capture,series.name,count(x.value) as n,
           avg(x.value::float8) filter (where position<=3) as recent3,
           avg(x.value::float8) filter (where position<=7) as recent7,
           avg(x.value::float8) filter (where position>7) as baseline,
           stddev_pop(x.value::float8) filter (where position>7) as baseline_sd,
           avg(x.value::float8) as mean,stddev_pop(x.value::float8) as sd
    from {{ captures }} c
    cross join (values ('Steps'),('Cal'),('Exercise'),('SpO2'),('VO2'),('Resp'),
                       ('Sleep'),('Deep'),('Awake'),('HRV'),('RHR')) series(name)
    left join lateral jsonb_array_elements_text(coalesce(nullif(c.inputs->'raw'->series.name,'null'::jsonb),'[]'))
        with ordinality x(value,position) on true
    group by c.profile,c.capture,series.name
), scores as (
    select *, coalesce((recent3-mean)/nullif(mean,0)*100,0) as change3,
           coalesce((recent7-baseline)/nullif(baseline,0)*100,0) as change7,
           case when baseline>0 and baseline_sd>0 then least(15,greatest(3,baseline_sd/baseline*100)) else 5 end as threshold
    from series
), components as (
    select *,case
      when name in ('Steps','Cal') and n>=3 then case when change3 > -10 then 2 when change3 > -30 then 1 else 0 end
      when name='Exercise' and n>=3 then case when recent3>=30 then 2 when recent3>=15 then 1 else 0 end
      when name='SpO2' and n>=3 then case when recent3>=95 then 2 when recent3>=92 then 1 else 0 end
      when name='VO2' and n>=3 then case when change3 > -3 then 2 when change3 > -8 then 1 else 0 end
      when name='Resp' and n>=3 then case when recent3 between 12 and 20 then 2 when recent3 between 10 and 24 then 1 else 0 end
      when name='HRV' and n>=9 then case when change7>threshold then 2 when change7 > -threshold then 1 else 0 end
      when name='RHR' and n>=9 then case when change7 < -2 then 2 when change7<3 then 1 else 0 end
    end as points from scores
), section_scores as (
    select profile,capture,case when name in ('Steps','Cal','Exercise') then 'activity'
                               when name in ('SpO2','VO2','Resp') then 'cardio' else 'recovery' end as section,
           bool_or(n>0 and name<>'Exercise') as present,avg(points)/2 as fraction
    from components where name not in ('Sleep','Deep','Awake') group by 1,2,3
), classified as (
    select s.profile,s.capture,section,case when fraction is null then ''
         when fraction >= case when section='recovery' then 0.75 else 0.7 end then
             case when section='recovery' and c.inputs->>'headline'='stress' then 'fair' else 'good' end
         when fraction>=0.4 then 'fair' else 'low' end as status
    from section_scores s join {{ captures }} c using(profile,capture) where present
), sleep_points as (
    select s.profile,s.capture,s.recent3,
       case when s.recent3>=7 then 3 when s.recent3>=6 then 2 when s.recent3>=5 then 1 else 0 end +
       case when d.n>=3 and d.recent3/nullif(s.recent3,0)*100>=15 then 2
            when d.n>=3 and d.recent3/nullif(s.recent3,0)*100>=10 then 1 else 0 end +
       case when a.n>=3 and a.recent3<0.5 then 1 else 0 end +
       case when coalesce((c.inputs->'raw'->>'SleepRegularityIndex')::float8>=75, s.n>=7 and s.sd<=0.5) then 1 else 0 end as points
    from series s join series d using(profile,capture) join series a using(profile,capture) join {{ captures }} c using(profile,capture)
    where s.name='Sleep' and s.n>0 and d.name='Deep' and a.name='Awake'
), statuses as (
    select * from classified union all
    select profile,capture,'sleep',case when points>=5 and recent3>=6 then 'good'
           when points>=3 or recent3>=5 then 'fair' else 'low' end from sleep_points
)
select c.profile,c.capture,coalesce(jsonb_object_agg(section,status) filter(where section is not null),'{}') as sections,
       case when count(*) filter(where status='low')>=2 then 'low'
            when count(*) filter(where status in ('fair','low'))>count(*) filter(where status='good') then 'fair'
            else 'good' end as overall
from {{ captures }} c left join statuses using(profile,capture) group by 1,2
{% endmacro %}
