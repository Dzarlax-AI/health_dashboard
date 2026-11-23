with stages as (
    select wake_date,source,stage,range_agg(tstzrange(start_at,end_at,'[)')) as merged
    from source_data.sleep_interval group by wake_date,source,stage
), spans as (
    select wake_date,source,stage,lower(s) as start_at,upper(s) as end_at
    from stages cross join lateral unnest(merged) s
), expected_minutes as (
    select wake_date,source,stage,sum(extract(epoch from end_at-start_at)::float8/60) as minutes
    from spans group by wake_date,source,stage
), stage_values as (
    select c.wake_date,k.stage,c.stage_basis,
           case c.stage_basis when 'episode_intervals' then e.minutes
             when 'same_source_day_summary' then s.observed_minutes end as expected,
           k.minutes as projected
    from archive_formula.semantic_sleep_continuity_v1 c
    cross join lateral (values ('sleep_deep',deep_minutes),('sleep_rem',rem_minutes),('sleep_core',core_minutes),
        ('sleep_awake',awake_minutes),('sleep_unspecified',unspecified_minutes)) k(stage,minutes)
    left join expected_minutes e on (c.wake_date,c.source,k.stage)=(e.wake_date,e.source,e.stage)
    left join source_data.sleep_stage_day s on (c.wake_date,c.source,k.stage)=(s.date,s.source,s.metric_name)
), awake as (
    select a.wake_date,count(*) as bouts from spans a where a.stage='sleep_awake'
      and exists (select from spans b where (b.wake_date,b.source,b.end_at)=(a.wake_date,a.source,a.start_at) and b.stage<>'sleep_awake')
      and exists (select from spans b where (b.wake_date,b.source,b.start_at)=(a.wake_date,a.source,a.end_at) and b.stage<>'sleep_awake')
    group by a.wake_date
), observed_awake as (
    select c.awakening_count, case when c.continuity_reason='continuous_recording' then coalesce(a.bouts,0) end as expected
    from archive_formula.semantic_sleep_continuity_v1 c left join awake a using(wake_date)
)
select json_build_object('stage_rows',(select count(*) from stage_values),
    'stage_measured',(select count(*) from stage_values where expected is not null),
    'stage_differences',(select count(*) from stage_values where expected is distinct from projected),
    'awake_rows',(select count(*) from observed_awake),
    'awake_measured',(select count(*) from observed_awake where expected is not null),
    'awake_differences',(select count(*) from observed_awake where expected is distinct from awakening_count));
