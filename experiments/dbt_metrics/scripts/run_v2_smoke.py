#!/usr/bin/env python3
"""Compatibility preflight only: no real credentials or database connections."""
import argparse,json,os,pathlib,subprocess,tempfile
ROOT=pathlib.Path(__file__).resolve().parents[1]
PIN='2.0.6'
def main():
    parser=argparse.ArgumentParser();parser.add_argument('--dbt',required=True);args=parser.parse_args()
    binary=str(pathlib.Path(args.dbt).resolve())
    env=os.environ.copy();env.pop('DATABASE_URL',None);env.pop('DBT_ALLOW_EXPERIMENTAL_ADAPTERS',None)
    env.update(DBT_SEND_ANONYMOUS_USAGE_STATS='false',DO_NOT_TRACK='1')
    version=subprocess.run([binary,'--version'],env=env,capture_output=True,text=True,check=True).stdout.strip()
    if version!=f'dbt {PIN}':raise RuntimeError(f'Expected dbt {PIN}, got {version!r}')
    with tempfile.TemporaryDirectory(prefix='health-dbt-v2-') as name:
        work=pathlib.Path(name)
        (work/'dbt_project.yml').write_text("name: health_v2_smoke\nversion: '1.0.0'\nconfig-version: 2\nprofile: health_v2_smoke\n")
        (work/'profiles.yml').write_text("health_v2_smoke:\n  target: local\n  outputs:\n    local:\n      type: postgres\n      host: 127.0.0.1\n      port: 1\n      user: synthetic\n      password: synthetic\n      dbname: synthetic\n      schema: dbt_output\n      threads: 1\n")
        run=subprocess.run([binary,'debug','--project-dir',name,'--profiles-dir',name],cwd=name,env=env,capture_output=True,text=True,timeout=60)
        output=run.stdout+run.stderr
        unsupported="postgres" in output.lower() and ('not supported' in output.lower() or 'not yet supported' in output.lower())
        report={'version':version,'exit_code':run.returncode,'postgres_adapter_supported':False if unsupported else None,'benchmark_run':False,'scope':'adapter preflight, loopback port 1, synthetic credentials; no SQL or production data','output':output}
        artifact=ROOT/'artifacts/v2-smoke.json';artifact.parent.mkdir(exist_ok=True);artifact.write_text(json.dumps(report,indent=2)+'\n')
        print(output);print(f'Report: {artifact}')
        if unsupported:return 0
        raise RuntimeError('Postgres support remains unconfirmed; inspect result before database benchmark')
if __name__=='__main__':raise SystemExit(main())
