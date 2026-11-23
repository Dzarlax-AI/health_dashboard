"""Emit a local historical comparison using the persisted generic HRV observations.

The normalized generic key retains neither method nor trustworthy interval
boundaries. Daily compatibility uses original writer timestamps and sources.
"""
from jinja2 import Environment, StrictUndefined

from verify_archive_history import ROOT, comparison


def render():
    env = Environment(undefined=StrictUndefined)
    env.globals.update(config=lambda **kwargs: '', var=lambda key, default=None: default,
                       source=lambda schema, name: 'source_data.' + name, ref=lambda name: name)
    for name in ['daily_source_mean', 'daily_source_rows']:
        mod = env.from_string((ROOT / 'macros' / (name + '.sql')).read_text()).module
        env.globals[name] = getattr(mod, name)
    yield """
create view source_data.hrv_point as
select date,source,qty,quality,units as unit,'unknown'::text as method,
       'unattributed'::text as channel,'unknown'::text as time_kind,
       null::timestamptz as start_at,null::timestamptz as end_at
from source_data.metric_points where metric_name='heart_rate_variability';
create view source_data.daily_hrv as
select date,hrv_avg,exists(select from source_data.settings s where
     s.key='cache_maintenance_hrv_repair_v1:'||d.date) as repair_pending from source_data.daily_scores d;
create view source_data.calendar as select date from source_data.daily_scores
union select substring(date,1,10) from source_data.hrv_point;
"""
    for name in ['semantic_hrv_observation_v1', 'semantic_hrv_daily_v1']:
        sql = env.from_string((ROOT / 'models' / (name + '.sql')).read_text()).render()
        yield f'create temporary table {name} as {sql.strip()};'
    for metric, method in [('hrv_observation_v1', None), ('hrv_observation_unknown_v1','unknown'),
                           ('hrv_observation_rmssd_v1','rmssd'), ('hrv_observation_sdnn_v1','sdnn')]:
        where = '' if method is None else f"where method='{method}'"
        ref = "select date, source, qty as v from source_data.hrv_point " + where
        actual = "select date, source, qty as v from semantic_hrv_observation_v1 " + where
        yield comparison(metric, 'date,source', ref, actual)
    yield """
create temporary view reference_daily as
with stats as (
    select left(date,10) as day,
      avg(qty) filter(where quality='ok' and qty>0) as raw,
      percentile_cont(0.5) within group(order by qty) filter(where quality='ok' and
         (substring(date,12,2)>='23' or substring(date,12,2)<'09')) as fixed
    from source_data.metric_points where metric_name='heart_rate_variability' group by 1
), hours as (
    select left(date,10) as day,left(date,13) as hour,source,avg(qty order by date collate "C")::real as hr
    from source_data.metric_points where metric_name='heart_rate_variability' and qty>0 and quality='ok' group by 1,2,3
), per_device as (
    select day,source,avg(hr order by hour collate "C") as source_avg from hours group by 1,2
), per_day as (
    select day,avg(source_avg order by source collate "C")::real as avg_val from per_device group by 1
)
select c.date,stats.fixed,coalesce(stats.raw,case when not d.repair_pending then d.hrv_avg end) as morning,
       a.avg_val as recalculated, d.hrv_avg as stored
from source_data.calendar c left join source_data.daily_hrv d using(date)
left join stats on stats.day=c.date left join per_day a on a.day=c.date;
"""
    for metric, ref, actual in [
        ('hrv_fixed_clock_day_median_v1','fixed','fixed_window_median_ms'),
        ('hrv_morning_compat_v1','morning','morning_ms'),
        ('hrv_daily_live_compat_v1','recalculated','live_ms'),
        ('hrv_daily_legacy_compat_v1','recalculated','legacy_ms')]:
        yield comparison(metric, 'date', f'select date,{ref} as v from reference_daily',
                         f'select date,{actual} as v from semantic_hrv_daily_v1')


if __name__ == '__main__':
    print('\n'.join(render()))
