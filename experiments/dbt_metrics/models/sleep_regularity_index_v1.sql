{{ config(enabled=var('preferred_sleep_source_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with segments as (
    select c.profile, c.capture, c.preferred_source, greatest(7, c.window_days) as days, s.*
    from {{ source('preferred_sleep_source','sri_captures') }} c
    cross join lateral (
        select observed_at, source, metric_name, qty
        from {{ source('preferred_sleep_source','sri_segments') }} s
        where (s.profile,s.capture) = (c.profile,c.capture)
          and quality = 'ok' and qty > 0
          and observed_at::date >= c.anchor - greatest(7, c.window_days) - 1
          and observed_at::time <> time '00:00'
          and metric_name in ('sleep_total','sleep_deep','sleep_core','sleep_unspecified','sleep_rem')
    ) s
), totals as (
    select profile, capture, observed_at::date as day, source, preferred_source, sum(qty order by qty) as hours
    from segments where metric_name = 'sleep_total' group by 1,2,3,4,5
), ranked as (
    select *, row_number() over (partition by profile,capture,day order by
        coalesce(source=preferred_source and preferred_source<>'' and hours>1,false) desc,
        ((source like '%Apple Watch%' or source like '%Ultra%') and hours > 1) desc, hours desc, source collate "C") as preference
    from totals
), winners as (
    select profile,capture,day,source
    from ranked where preference = 1
), minutes as (
    select s.profile,s.capture,s.days, date_trunc('minute',s.observed_at) + n * interval '1 minute' as wall_time
    from segments s left join winners w on (w.profile,w.capture,w.day) = (s.profile,s.capture,s.observed_at::date)
    cross join lateral generate_series(0, floor(s.qty*60+0.5)::int - 1) as g(n)
    where coalesce(w.source, '') = '' or w.source = s.source
), masks as (
    select profile,capture,days,wall_time::date as day,
           range_agg(int4range(m, m+1)) as mask
    from minutes cross join lateral (select (extract(hour from wall_time)*60+extract(minute from wall_time))::int as m) minute
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
from {{ source('preferred_sleep_source','sri_captures') }} c
left join results r using(profile,capture)
