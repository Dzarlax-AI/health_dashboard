{% macro chip_feature_windows_v1(inputs) %}
with selected as (
    select i.case_id, i.as_of_date, i.epoch_start, day,
           (day->>'value')::float8 as value, i.as_of_date-(day->>'date')::date as age
    from {{ inputs }} i
    left join lateral jsonb_array_elements(i.days) day on (day->>'eligible')::boolean
        and (day->>'date')::date between i.as_of_date-540 and i.as_of_date
), windows as (
    select case_id, max(value) filter (where age=0) as today,
           array_agg(value order by age desc) filter (where included and age<=135) as adaptive,
           array_agg(value order by age desc) filter (where included) as slow,
           avg(value order by age) filter (where included and age<7) as mean7,
           avg(value order by age) filter (where included and age<30) as mean30,
           count(value) filter (where included and age<7) as n7,
           count(value) filter (where included and age<45) as n45,
           count(value) filter (where included and age<180) as n180,
           sum(value order by age) filter (where included and age<45) as sum45,
           sum(value*value order by age) filter (where included and age<45) as sqsum45
    from (select *, value is not null and (epoch_start is null or as_of_date-age>=epoch_start) as included from selected) v
    group by case_id
), stats as (
    select *,case when n45>=2 then sum45/n45 end as mean45,
        case when n45>=2 then sqrt(greatest(0,(sqsum45-(n45*(sum45/n45))*(sum45/n45))/(n45-1))) end as sd45
    from windows
)
select w.case_id,today,mean7,mean30,mean45,sd45,case when sd45>0 then (today-mean45)/sd45 end as z45,
       n7,n45,n180,coalesce(cardinality(adaptive),0) as adaptive_n,coalesce(cardinality(slow),0) as slow_n,
       e.adaptive_value,e.slow_value,today-e.adaptive_value as delta_adaptive,today-e.slow_value as delta_slow
from stats w
cross join lateral (
    -- Fold observations oldest first. Missing calendar days carry no weight.
    with recursive values_by_window(kind,series,alpha) as (
        values ('adaptive',w.adaptive,2::float8/46),('slow',w.slow,2::float8/181)
    ), ewma(kind,series,alpha,position,value) as (
        select kind,series,alpha,1,series[1] from values_by_window where cardinality(series)>0
        union all
        select kind,series,alpha,position+1,alpha*series[position+1]+(1-alpha)*value
        from ewma where position<cardinality(series)
    )
    select max(value) filter(where kind='adaptive') as adaptive_value,
           max(value) filter(where kind='slow') as slow_value
    from ewma where position=cardinality(series)
) e
{% endmacro %}
