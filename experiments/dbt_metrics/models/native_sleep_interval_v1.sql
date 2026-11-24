{{ config(enabled=var('tenant_native_catalog',false), materialized='ephemeral') }}
-- Match the archive verifier's unique same-source session to a complete,
-- plausible main night. A summary or ambiguous episode cannot identify stages.
with spans as (
    select source,zone,start_at,start_at+hours*interval '1 hour' as end_at,
           metric_name as stage,bool_and(whole_seconds) as whole_seconds
    from {{ ref('native_sleep_stage_points_v1') }}
    where summary_day is null and hours>0 and hours<=24
    group by source,zone,start_at,end_at,metric_name
), starts as (
    select source,start_at,count(*) as variants,bool_and(whole_seconds) as whole
    from spans group by source,start_at
), boundaries as (
    select *,lead(start_at) over w as next_start,lead(start_at,2) over w as later_start,
           lead(variants) over w as next_variants,lead(whole) over w as next_whole
    from starts window w as (partition by source order by start_at)
), aligned as (
    select s.source,s.zone,s.start_at,s.stage,
           case when s.whole_seconds and b.variants=1 and b.next_variants=1 and b.next_whole
                and b.next_start between s.end_at-interval '1 second' and s.end_at+interval '1 second'
                and (b.later_start is null or b.later_start>s.end_at+interval '1 second')
                then b.next_start else s.end_at end as end_at
    from spans s join boundaries b using(source,start_at)
    where s.end_at>s.start_at
), gaps as (
    select *,max(end_at) over (partition by source order by start_at,end_at,stage
                rows between unbounded preceding and 1 preceding) as prior_end
    from aligned
), episodes as (
    select *,sum(case when prior_end is null or start_at>prior_end+interval '30 minutes' then 1 else 0 end)
       over(partition by source order by start_at,end_at,stage rows unbounded preceding) as session
    from gaps
), asleep as (
    select source,session,(max(end_at) at time zone zone)::date::text as wake_date,
           range_agg(tstzrange(start_at,end_at,'[)')) as asleep
    from episodes where stage<>'sleep_awake' group by source,session,zone
), durations as (
    select source,session,wake_date,sum(extract(epoch from upper(span)-lower(span))) as seconds
    from asleep cross join lateral unnest(asleep) r(span) group by source,session,wake_date
), ranked as (
    select *,row_number() over w as rank,lead(seconds) over w as runner_up
    from durations window w as (partition by source,wake_date order by seconds desc,session)
)
select r.wake_date,e.source,e.start_at,e.end_at,e.stage
from ranked r join {{ source('source_data','completed_night_sleep') }} n using(wake_date,source)
join episodes e using(source,session)
where r.rank=1 and (r.runner_up is null or r.seconds-r.runner_up>60)
  and n.capture_completeness='complete' and n.duration_assessment='plausible'
  and abs(r.seconds-n.duration_hours*3600)<=60
