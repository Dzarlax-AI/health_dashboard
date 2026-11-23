{% macro sleep_regularity_result() %}
with totals as (
    select profile, capture, observed_at::date as day, source, preferred_source, sum(qty order by qty) as hours
    from segments where metric_name = 'sleep_total' group by 1,2,3,4,5
), ranked as (
    select *, row_number() over (partition by profile,capture,day order by
        coalesce(source=preferred_source and preferred_source<>'' and hours>1,false) desc,
        ((source like '%Apple Watch%' or source like '%Ultra%') and hours > 1) desc, hours desc, source collate "C") as preference
    from totals where hours>0 and hours<>'NaN'::float8
), winners as (
    select profile,capture,day,source
    from ranked where preference = 1
), intervals as (
    select s.profile,s.capture,s.days, date_trunc('minute',s.observed_at) as start_at,
           case when s.qty<'Infinity'::float8 then date_trunc('minute',s.observed_at)
               + floor(s.qty*60+0.5) * interval '1 minute' end as end_at
    from segments s left join winners w on (w.profile,w.capture,w.day) = (s.profile,s.capture,s.observed_at::date)
    where coalesce(w.source,'')='' or w.source=s.source
), masks as (
    select profile,capture,days,midnight::date as day,
           range_agg(int4range(greatest(0,extract(epoch from start_at-midnight)/60)::int,
                               least(1440,extract(epoch from end_at-midnight)/60)::int)) as mask
    from intervals
    cross join lateral generate_series(date_trunc('day',start_at),end_at-interval '1 minute', interval '1 day') d(midnight)
    where end_at>start_at
    group by 1,2,3,4
), bounded as (
    select *,row_number() over(partition by profile,capture order by day desc) as age from masks
), pairs as (
    select *,lag(mask) over(partition by profile,capture order by day) as prior from bounded where age<=days
), results as (
    select profile,capture,count(*)::int as nights,
           sum((select coalesce(sum(upper(r)-lower(r)),0) from unnest((mask-prior)+(prior-mask)) as d(r))) as mismatches
    from pairs group by 1,2
)
select c.profile,c.capture,coalesce(r.nights,0) as nights,
       case when coalesce(nights,0)<7 then 'insufficient_days' else 'available' end as status,
       case when nights>=7 then 200*(1-mismatches::float8/((nights-1)*1440))-100 end as sri
from captures c
left join results r using(profile,capture)
{% endmacro %}
