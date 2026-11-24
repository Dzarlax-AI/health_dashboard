#!/usr/bin/env python3
"""Emit PostgreSQL checks for a private archive with the nine non-step source metrics.

Restore metric_points, hourly_metrics, daily_scores and both published hdbt tables
in source_data. Run with psql -XAtq -v ON_ERROR_STOP=1. Only counts leave SQL.
The canonical dbt projection starts from raw observations and is compared to an
independent implementation. Existing cache values are compared separately.

Hour arrays: kind, comparison, metric, reference cells, candidate cells, differences.
Day arrays: kind, column, calendar days, recomputed model differences, stored-vs-raw,
published-vs-raw, stored-vs-published, stored-vs-replay-own-hours,
published-vs-replay-own-hours, retained non-null fallbacks, present canonical values.
The fresh field is checked even when both writers would retain an older value.
Use --repair-average-hours only with the two average metrics and their saved hours.
"""
from pathlib import Path

from jinja2 import Environment, StrictUndefined

PILOT = Path(__file__).resolve().parents[1]
SLEEP = ["sleep_total","sleep_deep","sleep_rem","sleep_core","sleep_awake","sleep_unspecified"]
METRICS = {"active_energy":"calories","heart_rate_variability":"hrv_avg","respiratory_rate":"resp_avg",
           **{m:m for m in SLEEP}}
AVGS = ("heart_rate_variability","respiratory_rate")
NAMES = ",".join("'"+n+"'" for n in METRICS)


def macros():
    env=Environment(undefined=StrictUndefined)
    for name in ("keep_sleep_point","hourly_avg_rows","hourly_sum_rows","daily_source_rows",
                 "daily_source_mean","daily_preferred_sum","daily_retained_sleep","daily_retained_values",
                 "legacy_sleep_selection_v1","daily_candidates"):
        mod=env.from_string((PILOT/"macros"/(name+".sql")).read_text()).module
        env.globals[name]=getattr(mod,name)
    return env.globals


def oracle_days(hours):
    # Independent source ranking and completeness gates. Reject mixed incomplete
    # nights as a unit, and round each persisted boundary to PostgreSQL real.
    return f"""with by_source as (
      select left(hour,10) as day_key,metric_name,source,
        avg(avg_val order by hour collate "C") mean_val,sum(avg_val order by hour collate "C") total
      from {hours} group by 1,2,3
    ), sleep as (
      select s.*,count(*) over(partition by day_key) num,min(total) over(partition by day_key) lo,
        max(total) over(partition by day_key) hi from by_source s where metric_name='sleep_total'
    ), ranked as (
      select *,row_number() over(partition by day_key order by
        case when num>1 and lo>1 and hi>lo*1.4 then 0 when source like '%Ultra%' or source like '%Apple Watch%' then 0
             when source like '%RingConn%' then 1 else 2 end,
        case when num>1 and lo>1 and hi>lo*1.4 then total end asc,total desc,source) pick from sleep
    ), chosen as (
      select r.day_key,r.source from ranked r join by_source b using(day_key,source) where pick=1
      group by r.day_key,r.source,r.num having r.num=1 or
      count(*) filter(where b.metric_name in ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake'))=5 or
      count(*) filter(where b.metric_name in ('sleep_total','sleep_unspecified'))=2
    ), raw_daily as (
      select s.day_key,s.metric_name,case when s.metric_name in ('heart_rate_variability','respiratory_rate')
        then avg(s.mean_val order by s.source collate "C")::real
        when s.metric_name='active_energy' then coalesce(max(s.total) filter(where s.source like '%Ultra%' or s.source like '%Apple Watch%'),
             max(s.total) filter(where s.source like '%iPhone%'),max(s.total))::real
        else max(s.total) filter(where c.source=s.source)::real end v
      from by_source s left join chosen c on c.day_key=s.day_key group by 1,2
    ) select d.date,{",".join(f"max(v) filter(where metric_name='{m}') as {c}" for m,c in METRICS.items())}
      from days d left join raw_daily r on r.day_key=d.date group by d.date"""


def emit():
    f=macros()
    yield "begin; set local search_path=pg_temp,source_data,pg_catalog; set local jit=off; set local timezone='UTC';"
    yield f"""create temp view points as select * from source_data.metric_points where metric_name in ({NAMES});"""
    yield """create temp table days as select date from source_data.daily_scores union select date from source_data.hdbt_daily_metrics
      union select distinct left(date,10) from points;"""
    yield f"""create temp table model_hours as with valid_points as (
      select p.* from points p where left(metric_name,6)<>'sleep_' or {f["keep_sleep_point"]("points","p")}
    ), sum_points as (select * from valid_points where metric_name not in {AVGS}),
    avg_points as (select * from valid_points where metric_name in {AVGS})
    {f["hourly_avg_rows"]("avg_points")} union all {f["hourly_sum_rows"]("sum_points",True)};"""
    yield f"""create temp table oracle_hours as with pos as materialized (
      select *,left(date,10) day_key,left(date,13)||':00' hour_key,left(date,16) minute_key
      from points where quality='ok' and qty>0
    ), midnight as (select distinct metric_name,source,day_key from pos
      where left(metric_name,6)='sleep_' and substring(date,12,8)='00:00:00'),
    minutes as (
      select p.metric_name,p.source,p.hour_key,p.minute_key,max(p.qty)::real high,min(p.qty)::real low
      from pos p left join midnight m using(metric_name,source,day_key)
      where p.metric_name not in {AVGS} and (m.source is null or substring(p.date,12,8)='00:00:00')
      group by 1,2,3,4)
    select metric_name,hour_key as hour,source,avg(qty order by date collate "C")::real avg_val,
      min(qty)::real min_val,max(qty)::real max_val,count(*)::int sample_count
      from pos where metric_name in {AVGS} group by 1,2,3
    union all select metric_name,hour_key,source,sum(high order by minute_key collate "C")::real,
      min(low)::real,max(high)::real,count(*)::int from minutes group by 1,2,3;"""
    priority = "case when st.source like '%Ultra%' or st.source like '%Apple Watch%' then 0 when st.source like '%RingConn%' then 1 else 2 end"
    selection=f["legacy_sleep_selection_v1"]("per_source",priority,"1.0","1.4",",".join("'"+m+"'" for m in SLEEP[:5]),5,"'sleep_total','sleep_unspecified'",2)
    for tag,relation in [('raw','model_hours'),('cached','source_data.hourly_metrics'),('published','source_data.hdbt_hourly_metrics')]:
        yield f"""create temp table fresh_{tag} as with daily_steps as (
          select null::text as day,null::real as source_total where false),
          per_source as ({f['daily_source_rows'](relation,'true')}),{selection},
          daily_picks as (select p.date,p.source as picked_source,g.complete from picked p join sleep_gate g using(date))
          select * from ({f['daily_candidates']("source like '%Ultra%' or source like '%Apple Watch%'","source like '%iPhone%'")}) scored;"""
    yield "create temp table oracle_daily as "+oracle_days("oracle_hours")+";"
    # Compare each complete hourly record, including its key and multiplicity.
    for tag,rel in (('canonical','model_hours'),('stored','source_data.hourly_metrics'),('published','source_data.hdbt_hourly_metrics')):
        yield f"""select json_build_array('hour','{tag}',coalesce(a.metric_name,b.metric_name),count(a.hour),count(b.hour),
          count(*) filter(where a.hour is null or b.hour is null or
            row(a.avg_val,a.min_val,a.max_val,a.sample_count) is distinct from row(b.avg_val,b.min_val,b.max_val,b.sample_count)))
          from oracle_hours a full join {rel} b using(metric_name,hour,source) group by coalesce(a.metric_name,b.metric_name);"""
    for metric,col in METRICS.items():
        # If the fresh night failed its completeness gate, keep the complete
        # prior snapshot. Missing components of a freshly selected night stay NULL.
        def retained(alias):
            gate="sleep_total" if metric in SLEEP else col
            return f"case when {alias}.{gate} is not null then {alias}.{col} else d.{col} end"
        yield f"""select json_build_array('day','{col}',count(*),
          count(*) filter(where a.{col} is distinct from m.{col}),
          count(*) filter(where d.{col} is distinct from {retained('a')}),
          count(*) filter(where p.{col} is distinct from {retained('a')}),
          count(*) filter(where d.{col} is distinct from p.{col}),
          count(*) filter(where d.{col} is distinct from {retained('c')}),
          count(*) filter(where p.{col} is distinct from {retained('u')}),
          count(*) filter(where a.{col} is null and {retained('a')} is not null),
          count(*) filter(where {retained('a')} is not null))
          from days k join oracle_daily a using(date) join fresh_raw m using(date)
          join fresh_cached c using(date) join fresh_published u using(date)
          left join source_data.daily_scores d using(date) left join source_data.hdbt_daily_metrics p using(date);"""
    yield "rollback;"


def repair_hrv_and_respiration():
    """Exercise the writer on an isolated copy of its existing hourly cache.

    The production SQL templates receive the same dbt projections as in
    aggregateSQLWith. The old daily value is only a diagnostic reference.
    """
    f = macros()
    env = Environment(undefined=StrictUndefined)
    env.globals.update(f, config=lambda **kwargs: "", var=lambda *args: True,
                       source=lambda _, table: table, ref=lambda _: "observations")
    model = env.from_string((PILOT/"models/semantic_hrv_daily_v1.sql").read_text()).render()
    writer_dir = PILOT.parents[1]/"internal/storage/sql/aggregates"
    avg = f["hourly_avg_rows"]("hourly_points")
    slice_sql = (writer_dir/"legacy_hourly_avg.sql").read_text().replace("{{SLEEP_DEDUP}}", "").replace(
        "{{FROM_CLAUSE}}", "").replace("{{CONFIRMED_INVALID_AVERAGE_HOURS}}",
        (writer_dir/"confirmed_invalid_hours.sql").read_text().replace("{{HOUR_FILTER}}", "")).replace("{{HOURLY_AVG_ROWS}}",avg)
    yield "begin; set local search_path=pg_temp,source_data,pg_catalog; set local jit=off;"
    yield """create temporary table hourly_metrics as select * from source_data.hourly_metrics;
      create unique index on hourly_metrics(metric_name,hour,source);"""
    for metric in AVGS:
        yield slice_sql.replace("$1", "'"+metric+"'")+";"
    yield """create temporary view observations as select *,left(date,10) writer_date,
      quality='ok' and qty>0 writer_valid,quality='ok' and qty between 4 and 300 analytic_quality,
      'unknown'::text method,'ms'::text unit from source_data.metric_points where metric_name='heart_rate_variability';
      create temporary view daily_hrv as select date,hrv_avg,false as repair_pending from source_data.daily_scores;
      create temporary view calendar as select date from daily_hrv;"""
    yield """create temporary table expected_hours as select metric_name,left(date,13)||':00' as hour,source,
      avg(qty order by date collate "C")::real avg_val,min(qty)::real min_val,max(qty)::real max_val,count(*)::int sample_count
      from source_data.metric_points where qty>0 and quality='ok' group by 1,2,3;"""
    yield """select json_build_array('repair',coalesce(a.metric_name,b.metric_name),count(a.hour),count(b.hour),
      count(*) filter(where a.hour is null or b.hour is null or row(a.avg_val,a.min_val,a.max_val,a.sample_count)
      is distinct from row(b.avg_val,b.min_val,b.max_val,b.sample_count))) from expected_hours a full join hourly_metrics b
      using(metric_name,hour,source) group by coalesce(a.metric_name,b.metric_name);"""
    yield f"""with candidate as ({model}), per_source as (
      select left(hour,10) as date,source,avg(avg_val order by hour collate "C") as value
      from expected_hours where metric_name='heart_rate_variability' group by 1,2
    ), expected as (select date,avg(value order by source collate "C")::real as value from per_source group by date)
    select json_build_array('hrv_daily_legacy_compat_v1',count(*),count(e.value),count(c.legacy_ms),
      count(*) filter(where c.legacy_ms is distinct from e.value),
      count(*) filter(where d.hrv_avg is distinct from e.value))
    from calendar a join candidate c using(date) join daily_hrv d using(date) left join expected e using(date);"""
    yield "rollback;"


if __name__ == "__main__":
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repair-average-hours",action="store_true")
    args = parser.parse_args()
    print("\n".join(repair_hrv_and_respiration() if args.repair_average_hours else emit()))
