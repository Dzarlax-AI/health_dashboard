{% macro wake_candidate_result_v1(cases, segments) %}
with wake_cases as ({{ cases }}), segment_inputs as ({{ segments }}), segments as (
    select s.profile,s.capture,c.target_day,c.zone,c.preferred_source,s.source,s.metric,s.start_at,s.hours,s.received_at,s.quality,
           s.ends_at,s.start_at at time zone c.zone as local_start
    from wake_cases c join segment_inputs s using(profile,capture)
    where s.source <> '' and s.hours > 0 and s.quality='ok'
      and s.metric in ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake')
), normal as (
    select *, ends_at at time zone zone as local_end from segments
    where date_trunc('second',local_start)::time <> time '00:00'
), eligible as (
    select * from normal where local_end::date=target_day
      and date_trunc('minute',local_end)::time between time '03:00' and time '15:00'
), normal_sources as (
    select profile,capture,source,'detailed_stage_end'::text as signal,1 as signal_priority,
           sum(hours order by hours) filter(where metric<>'sleep_awake') as source_hours,
           max(ends_at) as wake,max(received_at) as latest_ingest
    from eligible where metric<>'sleep_total' group by profile,capture,source
    having count(*) filter(where metric<>'sleep_awake')>0
    union all
    select profile,capture,source,'raw_sleep_total_end',2,sum(hours order by hours),max(ends_at),max(received_at)
    from eligible where metric='sleep_total' group by profile,capture,source
), midnight_groups as (
    select profile,capture,source,target_day,zone,start_at,
           sum(hours order by hours) filter(where metric='sleep_total') as asleep,
           start_at+sum(ends_at-start_at order by hours) as ends_at,max(received_at) as received_at
    from segments where local_start::date=target_day and local_start::time=time '00:00'
      and metric in ('sleep_total','sleep_awake')
    group by profile,capture,source,target_day,zone,start_at
), midnight_sources as (
    select profile,capture,source,'midnight_summary' as signal,3 as signal_priority,
           sum(asleep order by asleep) as source_hours,max(ends_at) filter(where valid) as wake,
           (array_agg(received_at order by ends_at desc) filter(where valid))[1] as latest_ingest
    from (select *, ((ends_at at time zone zone)::date=target_day and
          date_trunc('minute',ends_at at time zone zone)::time between time '03:00' and time '15:00') as valid
          from midnight_groups where asleep>0) g
    group by profile,capture,source
), ranked as (
    select s.*, c.preferred_source,
      row_number() over(partition by s.profile,s.capture order by signal_priority,
        coalesce(source=c.preferred_source and source_hours>1 and wake is not null,false) desc,
        case when source_hours>1 and (source like '%Apple Watch%' or source like '%Ultra%') then 1
             when source_hours>1 and source like '%RingConn%' then 2 else 3 end,
        source_hours desc,source collate "C") as priority
    from (select * from normal_sources union all select * from midnight_sources) s
    join wake_cases c using(profile,capture)
    where coalesce(c.preferred_source,'')='' or s.wake is not null
), chosen as (
    select profile,capture,source,signal,wake,latest_ingest,
      case when coalesce(preferred_source,'')='' then 'automatic'
           when source=preferred_source and source_hours>1 and wake is not null then 'preferred'
           else 'preferred_unavailable' end as source_selection_reason
    from ranked where priority=1
)
select c.profile,c.capture, c.target_day,
       case when wake is null then 'no_data' else 'available' end as state,
       case when wake is not null then source end as source,
       case when wake is not null then signal end as signal,
       wake as candidate_wake,latest_ingest,
       case when wake is not null then source_selection_reason end as source_selection_reason
from wake_cases c left join chosen using(profile,capture)
{% endmacro %}
