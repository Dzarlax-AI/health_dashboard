"""Verify each reconstructible history score and band on an offline profile.

The input table history_daily(day date,hrv float8,rhr float8,sleep float8)
contains the application history reader's effective daily observations. The
source projection is printed with --source-sql. After restoring its result,
pass --container with a PostgreSQL container running with networking disabled.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

from jinja2 import Environment, StrictUndefined
from verify_archive_history import ROOT

SOURCE = """
with vital as (
    select left(date,10)::date as day,
      avg(qty) filter(where metric_name='heart_rate_variability') hrv,
      avg(qty) filter(where metric_name='resting_heart_rate') rhr
    from metric_points where metric_name in ('heart_rate_variability','resting_heart_rate')
      and quality='ok' and qty>0 group by 1
), sources as (
    select left(date,10)::date as day,source,sum(qty) total from metric_points
    where metric_name='sleep_total' and quality='ok' and qty>0 group by 1,2
), nights as (
    select day,case when count(*)>1 and min(total)>1 and max(total)>min(total)*1.4 then min(total)
      else coalesce(max(total) filter(where source like '%Apple Watch%' or source like '%Ultra%'),
                    max(total) filter(where source like '%RingConn%'),max(total)) end::float8 sleep
    from sources group by day
)
select day,hrv,rhr,sleep from vital full join nights using(day) order by day
"""


def comparison(container):
    if subprocess.check_output(['docker','inspect','-f','{{.HostConfig.NetworkMode}}',container],text=True).strip()!='none':
        raise ValueError('requires an offline local archive')

    def sql(statement):
        result = subprocess.run(['docker','exec','-i',container,'psql','-U','postgres','-XqAt','-v','ON_ERROR_STOP=1'],
                                input=statement,text=True,capture_output=True)
        if result.returncode:
            raise RuntimeError('archive history SQL failed')
        return result.stdout.strip()

    env=Environment(undefined=StrictUndefined)
    for name in ('readiness_effective_eligibility_v1','readiness_hrv_math_v1','readiness_score_v1'):
        macro=env.from_string((ROOT/'macros'/(name+'.sql')).read_text()).module
        env.globals[name]=getattr(macro,name)
    fields=('hrv','rhr','sleep')
    arrays=','.join(f'case when anchor.{f} is not null then array_agg(p.{f} order by p.day desc) filter(where p.{f} is not null) end as {f}' for f in fields)
    statement=f"""
        with inputs as (select anchor.day::text case_id,'archive'::text profile,anchor.day target_date,
            'history'::text reader,false hrv_present,false rhr_present,false sleep_present,
            false low_sleep_quality,false hrv_provisional,0 hrv_samples,''::text headline,''::text illness,true captured,{arrays}
            from history_daily anchor join history_daily p on p.day between anchor.day-29 and anchor.day
            group by anchor.day,anchor.hrv,anchor.rhr,anchor.sleep),
        eligible as ({env.globals['readiness_effective_eligibility_v1']('inputs')}),
        hrv as ({env.globals['readiness_hrv_math_v1']('eligible')}),
        captures as (select *,weighted_z hrv_weighted_z,component_weight hrv_weight,skip_reason hrv_reason from hrv),
        scores as ({env.globals['readiness_score_v1']('captures')})
        select coalesce(json_object_agg(case_id,json_build_array(raw_score,band)),'{{}}') from scores
    """
    candidates=json.loads(sql(statement))
    daily=sql("select coalesce(json_agg(to_jsonb(d) order by day),'[]') from history_daily d")
    with tempfile.TemporaryDirectory(prefix='health-readiness-history-') as tmp:
        path=Path(tmp)/'daily.json'
        path.write_text(daily)
        result=subprocess.run(['go','test','./internal/storage','-run','^TestArchiveReadinessHistoryOracle$','-count=1'],
                              cwd=ROOT.parents[1],env={**os.environ,'HEALTH_HISTORY_ORACLE_INPUT':str(path)},capture_output=True)
        if result.returncode:
            raise RuntimeError('Go archive history oracle failed')
        expected=json.loads((path.parent/'scores.json').read_text())
    for idx,name in enumerate(('score','band')):
        mismatches=sum(day not in candidates or day not in expected or expected[day][idx]!=candidates[day][idx]
                       for day in expected.keys()|candidates.keys())
        print(json.dumps(['readiness_history.'+name,len(expected),len(candidates),len(candidates),mismatches]))
    if expected!=candidates:
        raise RuntimeError('history replay differs')


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    source=parser.add_mutually_exclusive_group(required=True)
    source.add_argument('--source-sql',action='store_true')
    source.add_argument('--container')
    args=parser.parse_args()
    if args.source_sql:
        print(SOURCE+';')
    else:
        comparison(args.container)
