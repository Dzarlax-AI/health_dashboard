{% macro sleep_architecture_days() %}
with observed as (
    select distinct day from segments
), candidates as (
    select * from segments where metric<>'sleep_awake'
      and end_ns > -21600000000000 and start_ns < 43200000000000
), totals as (
    select day,source,preferred,sum(hours order by ordinal) as hours
    from candidates group by day,source,preferred
), winners as (
    select distinct on(day) day,source from totals where hours>0
    order by day,(source=preferred and preferred<>'' and hours>1) desc,
        case when hours>1 and (source like '%Apple Watch%' or source like '%Ultra%') then 0
             when hours>1 and source like '%RingConn%' then 1 else 2 end,
        hours desc,source collate "C"
), asleep as (
    select c.* from candidates c join winners using(day,source)
), night as (
    select day,source,min(start_ns) as first_ns,max(end_ns) as last_ns,sum(hours order by ordinal) as hours,
           bool_or(metric<>'sleep_unspecified' and hours>0) as staged
    from asleep group by day,source
), selected as (
    select day,start_ns,end_ns,false as awake from asleep
    union all
    select s.day,s.start_ns,s.end_ns,true from segments s join night n using(day,source)
    where s.metric='sleep_awake' and s.end_ns>n.first_ns and s.start_ns<n.last_ns
), ordered as (
    select *,max(end_ns) over(partition by day,awake order by start_ns,end_ns
        rows between unbounded preceding and 1 preceding) as prior_end
    from selected
), numbered as (
    select *,sum((prior_end is null or start_ns>prior_end)::int) over
        (partition by day,awake order by start_ns,end_ns) as bout
    from ordered
), bouts as (
    select day,awake,bout,(max(end_ns)-min(start_ns))::float8 / 3600000000000 as hours,
           bool_or(start_ns-prior_end>1800000000000) as follows_gap
    from numbered group by day,awake,bout
), stats as (
    select day,sum(hours order by bout) filter(where awake) as waso,
           count(*) filter(where awake) as explicit_bouts,
           count(*) filter(where not awake and follows_gap) as gaps,
           max(hours) filter(where awake) as longest
    from bouts group by day
)
select o.day,coalesce(n.source,'') as source,coalesce(n.hours,0) as asleep_hours,
       case when staged then coalesce(waso,0) else 0 end as waso,
       case when staged then explicit_bouts else 0 end as explicit_bouts,
       case when staged then gaps else 0 end as gaps,
       case when staged then coalesce(longest,0) else 0 end as longest,
       case when staged then (explicit_bouts+gaps)/n.hours else 0 end as fragmentation,
       coalesce(staged,false) as eligible,
       case when n.source is null then 'architecture_segments_missing'
            when not staged then 'coarse_only_source' else 'ok' end as reason,
       case when staged then 'high' else 'unavailable' end as confidence
from observed o left join night n using(day) left join stats using(day)
{% endmacro %}
