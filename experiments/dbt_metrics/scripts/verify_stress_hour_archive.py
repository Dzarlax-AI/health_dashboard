#!/usr/bin/env python3
"""Replay hourly stress on archived days with a provable default awake window.

Use the tenant search path and report timezone. Set psql's from_day and until_day
to the anchor-date range. Only counts leave the SQL statement.
"""
from pathlib import Path

from jinja2 import Environment, StrictUndefined

PROJECT = Path(__file__).resolve().parents[1]


def query():
    env = Environment(undefined=StrictUndefined)
    env.globals.update(config=lambda **kwargs: "", var=lambda k, d=None:d,
                       source=lambda _,name:name)
    model = env.from_string((PROJECT/"models/stress_hour_z_v1.sql").read_text()).render()
    return f"""with days as materialized (
      select date::date as day from daily_scores where date>=:from_day and date<:until_day
    ), sleep_days as materialized (
      select distinct left(date,10)::date as day from metric_points
      where metric_name in ('sleep_deep','sleep_rem','sleep_core','sleep_unspecified') and quality='ok'
      and qty>0 and substring(date,12,8)<>'00:00:00'
      and date>=((:from_day::date-2)::text) and date<((:until_day::date+3)::text)
    ), affected as (
      select distinct s.day+n as day from sleep_days s cross join generate_series(-3,2) n
    ), imputed as materialized (
      select d.day from days d left join affected s using(day) where s.day is null
    ), retained_hr as materialized (
      select left(date,10)::date as day,date,qty from metric_points
      where metric_name='heart_rate' and quality='ok' and date>=:from_day and date<:until_day
    ), points as (
      select d.day,m.date,m.date::timestamptz as ts,m.qty from imputed d join retained_hr m using(day)
      where m.date>=to_char((d.day+time '07:00')::timestamptz,'YYYY-MM-DD HH24:MI:SS TZHTZM')
      and m.date<to_char((d.day+time '22:00')::timestamptz,'YYYY-MM-DD HH24:MI:SS TZHTZM')
    ), buckets as materialized (
      select day,date_trunc('hour',ts) as hour_start,floor(extract(minute from ts)/5) as bucket,
        min(qty) as minimum,min(left(date,13)) as writer_hour from points group by 1,2,3
    ), covered as materialized (
      select day from buckets group by day having count(distinct writer_hour)>=8
    ), baselines as materialized (
      select d.day,stats.median as baseline,greatest(stats.sd,3.0) as sd
      from covered d cross join lateral (
        with samples as materialized (select avg_val::float8 value from hourly_metrics
          where metric_name='heart_rate' and hour>=(d.day-30)::text||' 00' and hour<d.day::text||' 00'
          and avg_val>'-Infinity'::float8 and avg_val<'Infinity'::float8),
        mid as (select count(*) n,percentile_cont(0.5) within group(order by value) median from samples)
        select n,median,(select percentile_cont(0.5) within group(order by abs(value-median))*1.4826 from samples) sd from mid
      ) stats where stats.n>=3
    ), hourly as (
      select day,hour_start,count(*)>=3 as coverage_ok,percentile_cont(0.5) within group(order by minimum) hour_hr
      from buckets group by 1,2
    ), series as materialized (
      select b.day,h.hour_start,coalesce(s.coverage_ok,false) coverage_ok,s.hour_hr,b.baseline,b.sd
      from baselines b cross join lateral generate_series((b.day+time '07:00')::timestamptz,
        (b.day+time '22:00')::timestamptz-interval '1 hour',interval '1 hour') h(hour_start)
      left join hourly s on s.day=b.day and s.hour_start=h.hour_start
    ), captured_inputs as (
      select 'local'::text profile,day capture,jsonb_build_object('stress_hours',jsonb_agg(to_jsonb(s))) inputs
      from series s group by day
    ), candidate as ({model}), original as (
      select day capture,hour_start,case when coverage_ok then
        case when sd<=0 or not (hour_hr>'-Infinity'::float8 and hour_hr<'Infinity'::float8
          and baseline>'-Infinity'::float8 and baseline<'Infinity'::float8 and sd<'Infinity'::float8) then 0.0
        else (hour_hr-baseline)/sd end end as value from series
    )
    select (select count(*) from days),(select count(*) from imputed),(select count(*) from baselines),
      count(o.capture),count(c.capture),count(c.hour_z),
      count(*) filter(where c.capture is null or o.capture is null or c.hour_z is distinct from o.value)
    from original o full join candidate c using(capture,hour_start)"""


if __name__ == "__main__":
    print(query().replace(":from_day", ":'from_day'").replace(":until_day", ":'until_day'")+";")
