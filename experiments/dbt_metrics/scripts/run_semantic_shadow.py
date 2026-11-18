"""Run a 90-day shadow in a new, socket-only local PostgreSQL cluster."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile

import psycopg2
import yaml

from night_card_report import write_night_cards
from verify_dbt_catalog import verify_dbt_catalog
from semantic_inputs import load_profile
from semantic_summary import summarize, summarize_night_cards

PILOT = Path(__file__).resolve().parents[1]
ROOT = PILOT.parents[1]
SEM = PILOT / 'semantic'


def execute(command, env):
    result = subprocess.run(command, env=env, cwd=ROOT, text=True, capture_output=True)
    if result.returncode:
        raise RuntimeError(f'{Path(str(command[0])).name} failed: {(result.stdout + result.stderr)[-1600:]}')
    return result.stdout


def validate_acl(cur):
    cur.execute("select relname, reloptions from pg_class where relnamespace='dbt_output'::regnamespace and relkind='v'")
    views = cur.fetchall()
    if not views or any('security_invoker=true' not in (opts or []) for _,opts in views):
        raise RuntimeError('view security options missing')
    # The second role has access to a view, but no rights on the source schema.
    cur.execute('create role peer_reader; create schema peer_data authorization peer_reader; create table peer_data.fixture(value int); alter table peer_data.fixture owner to peer_reader')
    cur.execute('grant usage on schema dbt_output to peer_reader; grant select on all tables in schema dbt_output to peer_reader')
    for role, relation in [('peer_reader','dbt_output.semantic_sleep_hrv_day_v1'),('shadow_reader','peer_data.fixture')]:
        cur.execute('savepoint acl_test')
        cur.execute('set local role '+role)
        try:
            cur.execute('select * from '+relation+' limit 1')
        except psycopg2.errors.InsufficientPrivilege:
            cur.execute('rollback to savepoint acl_test')
        else:
            raise RuntimeError('peer read succeeded')
    return len(views)


def run(lab, pg, profile, recovery, night_card_only=False, night_card_html=None):
    name = 'sem_profile' if profile else 'sem_synthetic'
    # No inherited DSN or PostgreSQL settings. Socket access requires directory ownership.
    env = {k:v for k,v in os.environ.items() if not k.startswith('PG') and k != 'DATABASE_URL'}
    env.update(DBT_SEND_ANONYMOUS_USAGE_STATS='false', GOPROXY='off')
    env.setdefault('GOCACHE', '/private/tmp/health-semantic-go-cache')
    env.update(SEMANTIC_SHADOW_PG_SOCKET=str(lab),SEMANTIC_SHADOW_PG_PORT='5433',
               SEMANTIC_SHADOW_PG_DB=name,SEMANTIC_SHADOW_PG_USER='shadow_reader')
    execute([pg/'initdb','-D',lab/'db','-U','shadow_admin','-A','trust','--no-locale','-E','UTF8'], env)
    execute([pg/'pg_ctl','-D',lab/'db','-l',lab/'pg.log','-o',f"-k {lab} -h '' -p 5433 -c timezone=Europe/Belgrade",'-w','start'],env)
    try:
        base = [pg/'psql','-X','-q','-h',lab,'-U','shadow_admin','-p','5433','-v','ON_ERROR_STOP=1']
        execute(base+['-d','postgres','-c','create database '+name],env)
        replay = None
        if recovery:
            patched, applied = lab/'replayed.json', lab/'applied.json'
            overlay = lab/'replay-overlay.json'
            target = ROOT/'internal/handler/night_repair_shadow_overlay_test.go'
            overlay.write_text(json.dumps({'Replace': {str(target):str(SEM/'sleep_replay_test.go.txt')}}))
            local_env = env | dict(REPLAY_SOCKET=str(lab),NIGHT_REPAIR_LOCAL_INPUT=str(recovery),
                                   NIGHT_REPAIR_LOCAL_RESULT=str(applied),NIGHT_REPAIR_PATCHED=str(patched))
            local_env.pop('READINESS_TEST_DSN',None)
            execute(['go','test','-overlay',overlay,'./internal/handler','-run','^TestLocalNightRecoveryDryRun$',
                     '-count=1'],local_env)
            replay = (recovery,applied,patched)
        conn = psycopg2.connect(host=str(lab),port=5433,user='shadow_admin',dbname=name)
        with conn, conn.cursor() as cur:
            cur.execute((SEM/'inputs.sql').read_text())
            cur.execute('create role shadow_dbt login; create role shadow_reader login; create schema dbt_output authorization shadow_dbt')
            cur.execute('grant usage on schema source_data to shadow_dbt,shadow_reader; grant select on all tables in schema source_data to shadow_dbt,shadow_reader')
            cur.execute('alter default privileges for role shadow_dbt in schema dbt_output grant select on tables to shadow_reader; grant usage on schema dbt_output to shadow_reader')
            if profile:
                provenance = load_profile(cur, profile, replay)
            else:
                cur.execute((SEM/'synthetic.sql').read_text())
                provenance = {'fixture':'semantic/synthetic.sql'}
            cur.execute('analyze')
        configs = {'type':'postgres','host':str(lab),'port':5433,'user':'shadow_dbt','password':'local',
                   'dbname':name,'schema':'dbt_output','threads':1}
        (lab/'profiles.yml').write_text(yaml.safe_dump({'health_dbt_metrics_pilot':{'target':'local','outputs':{'local':configs}}}))
        dbt = [PILOT/'.venv/bin/dbt','build','--project-dir',PILOT,'--profiles-dir',lab,'--target-path',lab/'target',
               '--log-path',lab/'logs','--vars','{sleep_duration_reference: true, semantic_shadow: true}',
               '--select','sleep_duration_reference_v1','semantic_*']
        execute(dbt,env)
        results = json.loads((lab/'target/run_results.json').read_text())['results']
        with conn, conn.cursor() as cur:
            acl = validate_acl(cur)
            cur.execute('set local role shadow_reader; set local search_path=dbt_output,pg_catalog')
            catalog = verify_dbt_catalog(cur, json.loads((lab/'target/manifest.json').read_text()))
            output = summarize_night_cards(cur) if night_card_only else summarize(cur)
            if night_card_html:
                write_night_cards(cur, night_card_html, recovery if replay else None)
            if replay and provenance['replay']['after'] != output['sleep_status'].get('available',0):
                raise RuntimeError('semantic comparison diverged from the repaired storage reader')
        edge_cases = []
        if not profile:
            for case in ('sleep_cases.sql','sleep_continuity_cases.sql','night_quality_cases.sql','hrv_cases.sql'):
                execute(base+['-d',name,'-f',SEM/case],env)
                edge_cases.append(case)
        sleep = execute(['go','test','./experiments/dbt_metrics','-count=1','-run','^TestSemanticSleepParity$','-v'],env)
        proof = re.search(r'dates=90 .*rounded_mismatches=\d+',sleep)
        if not proof:
            raise RuntimeError('missing sleep parity proof')
        quality = execute(['go','test','./experiments/dbt_metrics','-count=1','-run','^TestSemanticNightQualityParity$','-v'],env)
        quality_checks = re.findall(r'[\w-]+ rows=\d+ scored=\d+ mismatches=0', quality)
        if len(quality_checks) != 2:
            raise RuntimeError('missing main-night Go formula parity proof')
        hrv = None if night_card_only else json.loads(execute([sys.executable,PILOT/'scripts/run_semantic_hrv_oracle.py'],env))
        return dict(dataset=name,provenance=provenance,coverage=output,
                    checks=dict(catalog=catalog,dbt={r['unique_id']:r['status'] for r in results},edge_cases=edge_cases,
                    security_invoker_views=acl,cross_role_denials=2,sleep_go=proof[0],night_quality_go=quality_checks,hrv_go=hrv))
    finally:
        execute([pg/'pg_ctl','-D',lab/'db','-m','fast','-w','stop'],env)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--night-card-html', type=Path, help='write a private standalone readback of all dated cards')
    parser.add_argument('--night-card-only', action='store_true', help='Report only main-night coverage and run the dated sleep oracle')
    parser.add_argument('--private-extract',type=Path)
    parser.add_argument('--sleep-recovery',type=Path,help='owner-only minimized packet extract for local replay')
    parser.add_argument('--output',required=True,type=Path)
    parser.add_argument('--pg-bin',type=Path)
    args = parser.parse_args()
    if args.sleep_recovery and not args.private_extract:
        parser.error('--sleep-recovery requires --private-extract')
    if any(p and p.stat().st_mode & 0o077 for p in (args.private_extract,args.sleep_recovery)):
        parser.error('private extract must be owner-only')
    os.umask(0o077)
    pg = args.pg_bin or Path(subprocess.check_output(['pg_config','--bindir'],text=True).strip())
    if not (pg/'initdb').is_file():
        parser.error('select a PostgreSQL server installation using --pg-bin')
    with tempfile.TemporaryDirectory(prefix='health-semantic-',dir='/private/tmp') as tmp:
        result = run(Path(tmp), pg, args.private_extract, args.sleep_recovery, args.night_card_only,args.night_card_html)
    with tempfile.NamedTemporaryFile(mode='w',dir=args.output.parent, delete=False) as out:
        json.dump(result,out,indent=2,ensure_ascii=False,allow_nan=False)
        out.write('\n')
    os.replace(out.name,args.output)
    print(f"{result['dataset']}: {result['coverage']['days']} days; aggregate report saved")


if __name__ == '__main__':
    main()
