"""Compare dbt and Go over 30-day retrospective windows in a disposable local DB.

Input is source_data.daily_scores in a local Docker container with networking
disabled. Windows use retained completed-day rows, excluding nonpositive
samples. They do not reconstruct a delivered report or the live current day.
Only anonymous aggregate counts leave the disposable database and tempdir.
"""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

from jinja2 import Environment, StrictUndefined

from verify_archive_history import ROOT
from check_semantic_catalog import read_catalog

FIELDS = dict(HRV='hrv_avg', RHR='rhr_avg', Sleep='sleep_total', Deep='sleep_deep',
              REM='sleep_rem', Awake='sleep_awake', Cal='calories', Steps='steps',
              Exercise='exercise_min', SpO2='spo2_avg', Resp='resp_avg', VO2='vo2_avg')


def run(container, database):
    network = subprocess.check_output(['docker','inspect','-f','{{.HostConfig.NetworkMode}}',container], text=True).strip()
    if network != 'none':
        raise ValueError('expected an offline disposable Docker database')

    def sql(query):
        cmd = ['docker','exec','-i',container,'psql','-XAtq','-v','ON_ERROR_STOP=1','-U','postgres','-d',database]
        result = subprocess.run(cmd, input=query, text=True, capture_output=True)
        if result.returncode:
            raise RuntimeError('archive briefing SQL failed (private result suppressed)')
        return result.stdout.strip()

    nodes = ','.join(f"'{k}',jsonb_agg(d.{v}::float8 order by d.date desc) filter(where d.{v}>0)" for k,v in FIELDS.items())
    sql(f"""create schema briefing_inputs;
    create index on source_data.daily_scores(date);
    create table briefing_inputs.captured_inputs as
    select 'local'::text as profile,anchor.date as capture,
        jsonb_build_object('raw',values.raw || jsonb_build_object(
           'StepsToday',coalesce(anchor.steps,0)::float8,'ActiveEnergyToday',coalesce(anchor.calories,0)::float8,
           'StepsChronic28d',coalesce(ch.steps,0),'ActiveEnergyChronic28d',coalesce(ch.kcal,0))) as inputs
    from source_data.daily_scores anchor cross join lateral
        (select jsonb_build_object({nodes}) as raw from source_data.daily_scores d
         where d.date between (anchor.date::date-29)::text and anchor.date
           and (d.hrv_avg is not null or d.sleep_total is not null or d.steps is not null)) values
        cross join lateral (select avg(steps) filter(where steps>0) steps,
            avg(calories) filter(where calories>0) kcal from source_data.daily_scores
            where date between (anchor.date::date-28)::text and (anchor.date::date-1)::text) ch;""")
    with tempfile.TemporaryDirectory(prefix='health-briefing-private-') as directory:
        p = Path(directory) / 'inputs.json'
        p.write_text(sql("select coalesce(json_agg(inputs order by capture),'[]') from briefing_inputs.captured_inputs"))
        result = subprocess.run(['go','test','./internal/health','-run','^TestBriefingComponentsOracle$','-count=1'],
                                 cwd=ROOT.parents[1], env={**os.environ,'BRIEFING_COMPONENTS_FIXTURE':str(p),'HEALTH_ARCHIVE_ENERGY_ORACLE':'1'},capture_output=True)
        if result.returncode:
            raise RuntimeError('Go archive briefing oracle failed (private output suppressed)')
        expected = json.loads(Path(str(p)+'.expected').read_text())
    env = Environment(undefined=StrictUndefined)
    env.globals.update(config=lambda **kwargs:'',var=lambda k,default=None:default,
                       source=lambda s,n:'briefing_inputs.'+n,ref=lambda n:n)
    for m in ('weekly_sleep_round','weekly_sleep_analysis','briefing_metric_trends','energy_legacy_v1'):
        env.globals.update({k:v for k,v in vars(env.from_string((ROOT/'macros'/(m+'.sql')).read_text()).module).items()
                            if not k.startswith('_')})
    decision = json.dumps([{'readiness':r['readiness_score'],'headline':r['headline']} for r in (expected or [])])
    sql("create table briefing_inputs.legacy as with decision as (select row_number() over() idx,d.* from "
        "jsonb_to_recordset('" + decision.replace("'","''") + "'::jsonb) d(readiness int,headline text)), "
        "c as (select row_number() over(order by capture) idx, * from briefing_inputs.captured_inputs) "
        "select profile,capture as capture_id,d.readiness,d.headline,r.\"Sleep\" sleep,r.\"HRV\" hrv,r.\"RHR\" rhr,"
        "r.\"StepsToday\" steps,r.\"ActiveEnergyToday\" kcal,r.\"StepsChronic28d\" usual_steps,"
        "r.\"ActiveEnergyChronic28d\" usual_kcal from c join decision d using(idx) cross join lateral "
        "jsonb_to_record(c.inputs->'raw') r(\"Sleep\" float8[],\"HRV\" float8[],\"RHR\" float8[],"
        "\"StepsToday\" float8,\"ActiveEnergyToday\" float8,\"StepsChronic28d\" float8,\"ActiveEnergyChronic28d\" float8)")
    for m in ('semantic_energy_legacy_v1','briefing_components_v1','briefing_section_status_v1','briefing_metric_trends_v1','weekly_sleep_analysis_v1'):
        query = env.from_string((ROOT/'models'/(m+'.sql')).read_text()).render()
        sql('create table '+m+' as '+query)
    actual = json.loads(sql("""with trends as (select capture,jsonb_object_agg(metric,jsonb_build_array(
        pct7,status7,pct30,status30)) cards from briefing_metric_trends_v1 group by capture)
        select json_agg(jsonb_build_object('sections',jsonb_build_object('activity',a.activity,'cardio',a.cardio),
            'weekly_sleep',w.weekly_sleep,'legacy_energy',to_jsonb(e),'trends',coalesce(t.cards,'{}'::jsonb)) order by a.capture)
        from briefing_section_status_v1 a join weekly_sleep_analysis_v1 w using(capture) left join trends t using(capture) join semantic_energy_legacy_v1 e on e.capture_id=a.capture""") or "[]")
    catalog = read_catalog()
    energy_fields = {k: v['columns'][0] for k, v in catalog['dbt_bindings'].items()
                     if v['model'] == 'semantic_energy_legacy_v1'}
    trend_fields = {k: ['pct7','status7','pct30','status30'].index(v['columns'][0])
                   for k, v in catalog['dbt_bindings'].items() if v['model'] == 'briefing_metric_trends_v1'}
    output = {}
    def compare(key, a, b, ref_exists=True, new_exists=True):
        record = output.setdefault(key,[0,0,0,0])
        for i,n in enumerate([ref_exists,new_exists,new_exists and b is not None,a!=b or ref_exists!=new_exists]):
            record[i] += int(n)
    for old,new in zip(expected or [],actual or [],strict=True):
        for domain in ('activity','cardio'):
            compare(domain+'_section_status',old['sections'].get(domain),new['sections'].get(domain))
        for k in ('nights','total_avg','deep_avg','rem_avg','awake_avg','efficiency'):
            compare('sleep_summary.'+k,(old['weekly_sleep'] or {}).get(k),(new['weekly_sleep'] or {}).get(k))
        for key,column in energy_fields.items():
            compare(key,(old.get('legacy_energy') or {}).get(column),new['legacy_energy'].get(column))
        for card in old['trends'].keys() | new['trends'].keys():
            a=old['trends'].get(card,[None]*4); b=new['trends'].get(card,[None]*4)
            for key,idx in trend_fields.items():
                compare(key,a[idx],b[idx],card in old['trends'],card in new['trends'])
    for key, counts in sorted(output.items()):
        print(json.dumps([key,*counts]))


if __name__=='__main__':
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('container')
    parser.add_argument('database')
    args=parser.parse_args()
    run(args.container,args.database)
