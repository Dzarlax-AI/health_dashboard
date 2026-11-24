{{ config(enabled=var('semantic_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with episodes as (
    select wake_date,
           range_agg(tstzrange(start_at,end_at,'[)')) filter(where stage in ('deep','rem','core','unspecified'))
             - coalesce(range_agg(tstzrange(start_at,end_at,'[)')) filter(where stage='awake'), '{}'::tstzmultirange) as asleep,
           count(*) filter(where stage in ('deep','rem','core','unspecified')) as n_intervals
    from {{ ref('semantic_sleep_intervals_v1') }}
    group by wake_date
), candidates as (
    select e.wake_date,p.source,p.date,p.time_kind,p.qty,p.method,p.unit,p.quality,p.channel,p.start_at,p.end_at,
           case when p.analytic_status<>'eligible' then p.analytic_status
                when p.time_kind='interval' and not (e.asleep @> p.measurement) then 'crosses_sleep_boundary'
                else p.time_kind end as decision
    from episodes e join {{ ref('semantic_hrv_observation_v1') }} p
      on (p.time_kind='interval' and e.asleep && tstzmultirange(p.measurement))
      or (p.time_kind='timestamp' and e.asleep @> p.start_at)
      or (p.time_kind='bucket' and ((p.bucket is not null and e.asleep && tstzmultirange(p.bucket)) or (p.bucket is null and e.asleep @> p.start_at)))
), matched as (
    select wake_date,source,
           count(*) filter(where decision='interval') as interval_points,
           count(*) filter(where decision='timestamp') as timestamp_points,
           avg(qty) filter(where decision in ('interval','timestamp')) as mean_ms,
           encode(sha256(convert_to(jsonb_agg(jsonb_build_array(date, extract(epoch from start_at),
               extract(epoch from end_at), qty, method, unit, quality, channel, time_kind, decision)
               order by date)::text,'UTF8')), 'hex') as observations_version,
           string_agg(distinct decision,',' order by decision) as decisions
    from candidates group by 1,2
)
select c.date as wake_date,m.source,coalesce(e.n_intervals,0) as n_sleep_intervals,
       coalesce(m.interval_points,0) as interval_points,coalesce(m.timestamp_points,0) as timestamp_points,m.mean_ms,
       case when n.wake_date is null then 'no_canonical_night' when e.asleep is null or isempty(e.asleep) then 'no_sleep_interval'
            when m.interval_points>0 or m.timestamp_points>0 then 'available'
            else coalesce(m.decisions,'no_observations') end as reason, m.decisions, m.observations_version
from {{ ref('native_night_calendar_v1') if var('tenant_native_catalog',false) else source('source_data','calendar') }} c
left join {{ source('source_data','completed_night_sleep') }} n on n.wake_date=c.date
left join episodes e on e.wake_date=c.date
left join matched m on m.wake_date=c.date
