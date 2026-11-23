with merged as (
    select i.wake_date,i.stage,range_agg(tstzrange(start_at,end_at,'[)')) as ranges
    from source_data.sleep_interval i join source_data.completed_night_sleep n using(wake_date,source)
    group by i.wake_date,i.stage
), segments as materialized (
    select wake_date,stage,lower(r) as starts,upper(r) as ends
    from merged cross join lateral unnest(ranges) r
), conflicts as (
    select distinct a.wake_date from segments a join segments b
    on a.wake_date=b.wake_date and a.stage<b.stage and a.ends-b.starts>interval '1 second'
       and b.ends-a.starts>interval '1 second'
), marked as (
    select c.date,
        case when n.wake_date is null then 'no_night'
             when f.wake_date is not null then 'overlapping_stage_labels'
             when exists(select from merged m where m.wake_date=c.date and m.stage in ('sleep_deep','sleep_core','sleep_rem')) then 'recorded_episode_stages'
             when exists(select from merged m where m.wake_date=c.date) then 'episode_without_detailed_stages'
             when exists(select from source_data.sleep_stage_day s where s.date=c.date and s.source=n.source
                 and s.metric_name in ('sleep_deep','sleep_rem','sleep_core')) then 'day_summary_only'
             else 'no_detailed_stages' end as expected
    from source_data.calendar c left join source_data.completed_night_sleep n on n.wake_date=c.date
    left join conflicts f on f.wake_date=c.date
)
select json_build_object('rows',count(*),'diff',count(*) filter(where expected is distinct from stage_status))
from marked full join archive_formula.semantic_sleep_day_v1 d on marked.date=d.wake_date;
