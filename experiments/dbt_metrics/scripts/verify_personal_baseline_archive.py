#!/usr/bin/env python3
"""Compare the 30-day dbt baseline to the application source selectors on retained days.

Use the tenant's search_path and report timezone. Supply a half-open anchor date
range to split large archives. Queries emit counts and never individual samples.
"""
import argparse
from pathlib import Path

from jinja2 import Environment, StrictUndefined

PROJECT = Path(__file__).resolve().parents[1]
CHANNEL = {
    "hr_awake": ("hourly_metrics","hour","avg_val","metric_name='heart_rate'"),
    "hr_overnight": ("daily_scores","date","baseline_hr_overnight","baseline_hr_overnight is not null"),
    "hrv": ("metric_points","date","qty","metric_name='heart_rate_variability' and quality='ok' and (substring(date,12,2)>='23' or substring(date,12,2)<'09')"),
    "resp": ("metric_points","date","qty","metric_name='respiratory_rate' and quality='ok'"),
    "temp": ("metric_points","date","qty","metric_name='wrist_temperature' and quality='ok'"),
}
FIELDS = ("baseline_median","mad_sd","age_nanoseconds","sample_count","state")
IDS = ("median","madsd","newest_age","sample_count","state")


def query(channel):
    table,clock,column,predicate = CHANNEL[channel]
    env = Environment(undefined=StrictUndefined)
    env.globals.update(config=lambda **kw: "", var=lambda k, d=None:d,
                       source=lambda _,name: "input_captures" if name=="captures" else name)
    for macro in ("personal_baseline_stats","personal_baseline_result"):
        env.globals[macro] = getattr(env.from_string((PROJECT/"macros"/(macro+".sql")).read_text()).module,macro)
    model = env.from_string((PROJECT/"models/personal_baseline_v1.sql").read_text()).render()
    fmt = {"hr_awake":"YYYY-MM-DD HH24","hr_overnight":"YYYY-MM-DD"}.get(channel,"YYYY-MM-DD HH24:MI:SS TZHTZM")
    # The Go timestamp parser accepts dates, minute-local times and seconds
    # with explicit offsets. Check the archive clock shape before claiming parity.
    if table=="metric_points":
        pattern = "^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(\\.[0-9]{1,6})? [+-][0-9]{4}$"
    else:
        pattern = "^[0-9]{4}-[0-9]{2}-[0-9]{2}" + (" [0-9]{2}:[0-9]{2}" if channel=="hr_awake" else "") + "$"
    reference = env.globals["personal_baseline_stats"]()
    result = env.globals["personal_baseline_result"]()
    counter = ",".join(f"count(*) filter(where candidate.{f} is distinct from original.{f}) as {f}" for f in FIELDS)
    return f"""select count(*) as anchors,{counter},
      coalesce(sum(original.errors),0)::bigint as unsupported_samples
    from (select date::date anchor from daily_scores where date>=:from_day and date<:until_day) a
    cross join lateral (
      with input_captures as (select 'local'::text as profile,1 as capture,'{channel}'::text as channel,
        a.anchor,current_setting('TimeZone') as zone,30 as window_days),
      selected_samples as (select 'local'::text profile,'{channel}'::text channel,
        left({clock},10)::date writer_day,case when {clock} ~ '{pattern}' and pg_input_is_valid({clock},'timestamptz')
        then {clock}::timestamptz end observed_at,{column}::float8 as value from {table}
        where {predicate} and {clock} >= (a.anchor-30)::text and {clock}<a.anchor::text)
      select * from ({model}) candidate_model
    ) candidate
    cross join lateral (
      with source_samples as materialized (
        select {column}::float8 value,{clock} raw_clock from {table} where {predicate}
          and {clock}>=to_char((a.anchor-30)::timestamptz,'{fmt}') and {clock}<to_char(a.anchor::timestamptz,'{fmt}')
      ), samples as materialized (
        select value,case when raw_clock ~ '{pattern}' and pg_input_is_valid(raw_clock,'timestamptz') then raw_clock::timestamptz end observed_at
        from source_samples where value>'-Infinity'::float8 and value<'Infinity'::float8
      ), stats as ({reference}), summary as (
        select stats.*,'{channel}'::text channel,
          extract(epoch from (a.anchor::timestamptz-(select max(observed_at) from samples)))*1000000000 age_ns
        from stats
      ), result as ({result})
      select result.*,coalesce(age_ns,0)::bigint age_nanoseconds,
        (select count(*) from source_samples where value is null)+(select count(*) from samples where observed_at is null) errors
      from result
    ) original"""


if __name__ == "__main__":
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("--channel",choices=CHANNEL,required=True)
    args=p.parse_args()
    print(query(args.channel).replace(":from_day", ":'from_day'").replace(":until_day", ":'until_day'")+";")
