{{ config(enabled=var('complex_sleep_parity',false),materialized='view',post_hook="alter view {{ this }} set (security_invoker = true)") }}

with periods as (
    select c.profile, c.capture, c.wake_date, c.zone, c.wake_date - n as period_day
    from {{ source('complex_sleep','balance_capture') }} c cross join generate_series(0,13) n
), bounds as (
    select p.*, ((period_day - 1) + time '12:00') at time zone zone as starts_at,
           (period_day + time '12:00') at time zone zone as ends_at
    from periods p
), inputs as (
    select b.*, goal.hours as goal_hours, k.state as coverage,
           coalesce(e.total_hours,0)::double precision as total_hours, e.flagged
    from bounds b
    left join {{ source('complex_sleep','balance_coverage') }} k using(profile,capture,period_day)
    left join lateral (
        select g.hours from {{ source('complex_sleep','balance_goal') }} g
        where g.profile=b.profile and g.capture=b.capture and g.effective_date<=b.period_day
        order by g.effective_date desc limit 1
    ) goal on true
    left join lateral (
        -- Match time.Duration.Hours() for each clipped episode before adding it.
        select sum((duration.micros / 3600000000)::float8 +
                   (duration.micros % 3600000000)::float8 / 3600000000::float8
                   order by ep.start_at,ep.id) as total_hours,
               bool_or(ep.assessment='outlier') as flagged
        from {{ source('complex_sleep','balance_episode') }} ep
        cross join lateral (select (extract(epoch from
            least(ep.end_at,b.ends_at)-greatest(ep.start_at,b.starts_at))*1000000)::bigint as micros) duration
        where ep.profile=b.profile and ep.capture=b.capture
          and ep.end_at>b.starts_at and ep.start_at<b.ends_at
    ) e on true
)
select *, case when coverage is null then 'coverage_missing'
               when coverage='partial' then 'coverage_partial'
               when coverage='unknown' then 'coverage_unknown'
               when coverage<>'complete' then 'coverage_invalid'
               when goal_hours is null then 'goal_missing' end as reason,
       case when flagged or total_hours>0 and (total_hours<3 or total_hours>14)
            then 'outlier' else 'plausible' end as assessment
from inputs
