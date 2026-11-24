{{ config(enabled=var('tenant_native_catalog',false), materialized='ephemeral') }}
-- An explicit profile clock is required to separate midnight day summaries
-- from intervals. A missing or obsolete timezone leaves attribution unknown.
with zone as (
    select t.name from {{ adapter.quote(target.schema) }}.settings s
    join pg_timezone_names t on t.name=s.value where s.key='timezone'
), points as (
    select p.date,p.source,p.metric_name,p.qty::float8 as hours,z.name as zone,
           case when p.date ~ '(Z|[+-][0-9]{2}:?[0-9]{2})$' and pg_input_is_valid(p.date,'timestamptz')
                then p.date::timestamptz end as start_at
    from {{ source('source_data','metric_points') }} p cross join zone z
    where p.quality='ok' and p.source<>'' and p.metric_name in
        ('sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake')
      and lower(p.units) in ('h','hr','hrs','hour','hours')
)
select *,case when substring(date,12,8)='00:00:00' and extract(microseconds from start_at)::int=0
              then left(date,10)
              when (start_at at time zone zone)::time=time '00:00' then (start_at at time zone zone)::date::text
         end as summary_day,
       substring(date,20,1) not in ('.',',') and date_trunc('second',start_at)=start_at as whole_seconds
from points where start_at is not null
