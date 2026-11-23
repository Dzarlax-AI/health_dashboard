"""Compare contextual SDNN models on an archive with explicitly unattributed HRV.

Requires the normalized semantic inputs (calendar, workouts, attributed sleep
intervals and HRV points) and a verified source inventory in the same snapshot.
This checks missing results at their native keys; it cannot certify a measured
SDNN value. Typed positive controls live in semantic/hrv_cases.sql.
"""
import argparse
from zoneinfo import ZoneInfo

from jinja2 import Environment, StrictUndefined

from verify_archive_history import ROOT, comparison


def render(zone):
    ZoneInfo(zone)
    literal = zone.replace("'", "''")
    env = Environment(undefined=StrictUndefined)
    env.globals.update(config=lambda **kwargs: '', ref=lambda name: name,
                       source=lambda schema, name: 'source_data.' + name,
                       var=lambda key, default=None: zone if key == 'semantic_timezone' else default)
    yield """do $$ begin
      if exists(select from source_data.hrv_point where method is distinct from 'unknown'
         or time_kind not in ('unknown','timestamp','bucket') or end_at is not null)
      then raise exception 'archive contains attributable observations; use a measured-value oracle'; end if;
    end $$;"""
    for name in ('semantic_hrv_observation_v1','semantic_sleep_intervals_v1','semantic_hrv_night_v1',
                 'semantic_hrv_workout_evidence_v1','semantic_hrv_workout_windows_v1',
                 'semantic_hrv_workout_v1','semantic_hrv_after_workout_v1'):
        model = env.from_string((ROOT/'models'/(name+'.sql')).read_text()).render()
        yield f'create temporary table {name} as {model};'
    yield """create temporary view archive_candidate_clocks as
        select distinct source,start_at from source_data.hrv_point
        where time_kind in ('timestamp','bucket') and start_at is not null;"""
    night_keys = """select distinct i.wake_date,p.source from archive_candidate_clocks p
        join source_data.sleep_interval i on p.start_at>=i.start_at and p.start_at<i.end_at
        join source_data.completed_night_sleep n on n.wake_date=i.wake_date and n.source=i.source
        where replace(i.stage,'sleep_','') in ('deep','rem','core','unspecified','asleep')
        and not exists(select from source_data.sleep_interval a where a.wake_date=i.wake_date and a.source=i.source
            and a.stage in ('sleep_awake','awake') and p.start_at>=a.start_at and p.start_at<a.end_at)"""
    yield comparison('hrv_main_sleep_sdnn_v1','wake_date,source',
                     f'select c.date as wake_date,k.source,null::float8 v from source_data.calendar c left join ({night_keys}) k on k.wake_date=c.date',
                     'select wake_date,source,mean_ms v from semantic_hrv_night_v1')
    for metric, model in [('hrv_workout_sdnn_v1','semantic_hrv_workout_v1'),
                          ('hrv_after_workout_sdnn_v1','semantic_hrv_after_workout_v1')]:
        columns = ('interval_mean_ms','timestamp_mean_ms')
        start, end = ('w.start_at','w.end_at') if model=='semantic_hrv_workout_v1' else ('w.end_at',"w.end_at+interval '30 minutes'")
        expected = f"""select distinct w.id,p.source,component,null::float8 v from source_data.workout w
            join source_data.calendar c on c.date=(w.start_at at time zone '{literal}')::date::text
            cross join (values ('interval_mean_ms'),('timestamp_mean_ms')) f(component)
            left join archive_candidate_clocks p on w.end_at>w.start_at and p.start_at>={start} and p.start_at<{end}"""
        candidate = ' union all '.join(f"select id,source,'{col}' component,{col} v from {model}" for col in columns)
        yield comparison(metric,'id,source,component',expected,candidate)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--timezone', required=True)
    print('\n'.join(render(parser.parse_args().timezone)))
