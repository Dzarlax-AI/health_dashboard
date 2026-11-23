#!/usr/bin/env python3
"""Compare briefing classifications and signal calculations with real Go consumers."""
import json
import os
from pathlib import Path
import random
import subprocess
import tempfile
import time

from weekly_sleep_cases import weekly_sleep_inputs

ROOT = Path(__file__).resolve().parents[3]
PILOT = Path(__file__).resolve().parents[1]


def command(args, **kwargs):
    return subprocess.run(args, cwd=ROOT, check=True, text=True, **kwargs)


def fixtures():
    rng = random.Random(7341)
    rows = []
    for i in range(80):
        raw = {}
        for metric, lo, hi in [
            ('HRV', 30, 65), ('RHR', 45, 75), ('Sleep', 4, 9), ('Deep', 0, 2),
            ('Awake', 0, 1), ('Cal', 150, 900), ('Steps', 500, 12000),
            ('Exercise', 0, 45), ('SpO2', 88, 100), ('Resp', 9, 25), ('VO2', 25, 59),
        ]:
            n = 0 if i == 0 else rng.choice([0, 1, 3, 5, 7, 9, 15])
            raw[metric] = [float(rng.randint(lo, hi)) for _ in range(n)]
        if i % 5 == 2:
            raw['NightSleep'] = [7.5, 7, 8]
        if i % 3 == 1:
            raw['SleepRegularityIndex'] = rng.choice([50, 74, 75, 99])
        raw['StepsWithDates'] = [{'Date': '2000-01-02', 'Val': 4000}, {'Date': '2000-01-01','Val': 8000}]
        raw['HRVWithDates'] = [{'Date': '2000-01-02', 'Val': 54.4}, {'Date': '2000-01-03','Val': 39}]
        borders = ['', '100,120,140,160', '90,110,130,150'][i % 3]
        samples = [dict(Time='2000-01-02T08:' + minute + 'Z', Avg=bpm)
                   for minute, bpm in [('12:00', 160), ('01:00.5', 120), ('00:00', 99),
                                       ('01:55', 140), ('00:23', 100)]]
        if i % 5 == 0:
            samples += [dict(Time='2000-01-02T08:00:00.499999999Z', Avg=120),
                        dict(Time='2000-01-02T08:00:01.000000000Z', Avg=140)]
        if i % 5 == 1:
            # Larger unsorted arrays can reorder tied instants in the legacy Go sort.
            samples *= 4
            for j, sample in enumerate(samples):
                samples[j] = dict(sample, Avg=90 + (j % 5) * 20)
        raw.update(weekly_sleep_inputs(i))
        if i % 8:
            evidence = {key: dict(value=value, present=True, freshness="ok", confidence="final",
                                  evaluated_date="2000-01-02", source_date="2000-01-02", sample_count=1)
                        for key,value in (("SleepDuration",7.1),("SleepDeep",0.7),("SleepREM",1.1),
                                          ("SleepAwake",0.0),("SleepQuality",1.0))}
            if i % 8 == 2:
                evidence["SleepAwake"].pop("value")
            if i % 8 == 3:
                evidence["SleepREM"]["confidence"] = "low"
            if i % 8 == 4:
                evidence["SleepQuality"]["confidence"] = "provisional"
            if i % 8 == 5:
                evidence["SleepDeep"]["freshness"] = "stale"
            if i % 8 == 6:
                evidence["SleepDuration"]["value"] = 0
            if i % 8 == 7:
                evidence["SleepAwake"]["present"] = False
            raw["ReadinessEvidence"] = evidence
        day = dict(readiness_today=[49, 50, 79, 80][i % 4], today_guidance=None, energy_bank=None)
        if i % 3:
            day['energy_bank'] = dict(action_verdict='active_recovery')
        if i % 5 < 3:
            day['today_guidance'] = dict(action='rest')
        if i % 7 < 4:
            day['illness_suspicion'] = dict(confidence=['low','moderate','high'][i % 3])
        if i % 2:
            day['sleep'] = dict(latest_total=7.25)
            day['headline'] = dict(key="stress")
        if day.get('illness_suspicion') is not None:
            day['illness_suspicion']['signals'] = [
                dict(metric='resp',kind='metric',role='primary',category='respiratory',direction='high',
                     strength='mild',status='ok',contributes=True,value=15.0,baseline=14.0,
                     delta_abs=1.0,z_score=0.5,method='observed')]
        if day.get('energy_bank') is not None:
            day['energy_bank']['flags'] = [] if i % 2 else ['data_accruing']
        if i % 11:
            day['sleep_quality'] = dict(confidence=['final','partial'][i % 2])
        rows.append(dict(raw=raw, decision=day, zones=borders, hr_samples=samples if i % 7 else [],
                         workout_end='2000-01-02T08:' + ('10:00Z' if i % 4 else '13:00Z'), hour_z=[0, 1, 2, 2.5, 1.5, 1.25][:i % 7],
                         temp=i % 3, resp=i % 4, drop=i % 7 - 3, rhr=i % 4 / 2, day_hr=i % 3,
                         walking=[None, 49, 50, 180, 181][i % 5]))
    for i, row in enumerate(rows):
        row['stress_hours'] = [
            dict(hour_start='2000-10-29T02:00:00+02:00', coverage_ok=True, hour_hr=70, baseline=60, sd=5),
            dict(hour_start='2000-10-29T02:00:00+01:00', coverage_ok=True, hour_hr=55, baseline=60, sd=5),
            dict(hour_start='2000-10-29T03:00:00+01:00', coverage_ok=False, hour_hr=60, baseline=60, sd=5),
            dict(hour_start='2000-10-29T04:00:00+01:00', coverage_ok=True, hour_hr=60, baseline=60, sd=0),
        ]
        if i % 4:
            row['z_threshold'] = [1, 0.5, -0.25, 1.5][i % 4]
        if i % 8 == 3:
            row['hour_z'] = [2.1, 1.1, None, 2.0, 1.1]
        if i % 8 == 4:
            row['hour_z'] = [None, 1.01, 1.01, 1.01, 1.01]
        if i % 9 == 0:
            row['resp'] = None
    return rows


def main():
    rows = fixtures()
    with tempfile.TemporaryDirectory(prefix='briefing-dbt-') as tmp:
        lab = Path(tmp)
        source = lab / 'fixture.json'
        source.write_text(json.dumps(rows))
        command(['go','test','./internal/health','-run','^TestBriefingComponentsOracle$','-count=1'],
                env=dict(os.environ, BRIEFING_COMPONENTS_FIXTURE=str(source)))
        expected = json.loads(Path(str(source)+'.expected').read_text())
        command(['go','test','./experiments/dbt_metrics','-run','^TestCapturedDashboardSleepQuality$','-count=1'],
                env=dict(os.environ, BRIEFING_COMPONENTS_FIXTURE=str(source)))
        expected_quality = json.loads(Path(str(source)+'.quality').read_text())
        cid = command(['docker','run','--rm','-d','--tmpfs','/var/lib/postgresql/data',
                       '-p','127.0.0.1::5432','-e','POSTGRES_PASSWORD=synthetic-only',
                       'postgres:16-alpine'],capture_output=True).stdout.strip()
        try:
            port = int(command(['docker','port',cid,'5432/tcp'],capture_output=True).stdout.strip().split(':')[-1])
            for _ in range(100):
                if subprocess.run(['docker','exec',cid,'pg_isready','-h','127.0.0.1'],capture_output=True).returncode == 0:
                    break
                time.sleep(.2)
            else:
                raise RuntimeError("temporary PostgreSQL did not become ready")

            def sql(statement):
                return command(['docker','exec','-i',cid,'psql','-XAtq','-v','ON_ERROR_STOP=1','-U','postgres'],
                               input=statement,capture_output=True).stdout.strip()
            sql((PILOT / "fixtures/briefing_capture.sql").read_text())
            for i, (inp, oracle) in enumerate(zip(rows,expected,strict=True)):
                inp['headline'] = oracle.pop('headline')
                normalized = oracle.pop('zone_input')
                if i % 5 == 1 or i % 2 == 0:
                    inp['zone_input'] = normalized
            # All input values are locally generated. psql COPY keeps JSON separate from SQL.
            sql('COPY briefing_pilot.captured_inputs FROM STDIN;\n' + ''.join(
                f'profile-{i%2}\t{i//2}\t{json.dumps(row)}\n' for i,row in enumerate(rows)) + '\\.\n')
            (lab/'profiles.yml').write_text(f"""health_dbt_metrics_pilot:
  target: pilot
  outputs:
    pilot:
      type: postgres
      host: 127.0.0.1
      port: {port}
      user: postgres
      password: synthetic-only
      dbname: postgres
      schema: derived
      threads: 1
""")
            command([str(PILOT/'.venv/bin/dbt'),'build','--project-dir',str(PILOT),'--profiles-dir',tmp,
                     '--target-path',str(lab/'target'),'--log-path',str(lab/'logs'),'--vars','{briefing_components_shadow: true}',
                     '--select','briefing_section_status_v1','stress_hour_z_v1','captured_briefing_fields_v1','captured_illness_evidence_v1','captured_readiness_component_v1','captured_dashboard_sleep_quality_v1','briefing_components_v1','briefing_signals_v1','illness_suspicion_v1','briefing_metric_trends_v1','workout_hr_zones_v1','sleep_regularity_index_v1','sleep_architecture_days_v1','sleep_architecture_windows_v1','weekly_sleep_analysis_v1','oxygen_cluster_v1','daily_decision_boundary_v1'],env=dict(os.environ,DBT_SEND_ANONYMOUS_USAGE_STATS='false'))
            dsn = f'postgres://postgres:synthetic-only@127.0.0.1:{port}/postgres?sslmode=disable'
            command(['go', 'test', './internal/storage', '-run', '^Test(SleepRegularity|SleepArchitecture|OxygenCluster|IllnessClassification|BriefingProjection|StressSignalWriter)Shadow$|^TestIllnessClassificationReaders$', '-count=1'],
                    env=dict(os.environ, HEALTH_DB_TESTS='1', READINESS_TEST_DSN=dsn, BRIEFING_STORAGE_PILOT='1'))
            command(['go','test','./internal/handler','-run','^TestWorkoutZoneIngestSQL$','-count=1'],
                    env=dict(os.environ, HEALTH_DB_TESTS='1', READINESS_TEST_DSN=dsn))
            records = json.loads(sql("""with trends as (
              select profile,capture,jsonb_object_agg(metric,jsonb_build_array(pct7,status7,pct30,status30)) as vals
              from derived.briefing_metric_trends_v1 group by 1,2)
              select json_agg(to_jsonb(b)||to_jsonb(s)||to_jsonb(z)||(to_jsonb(d)-'signal_keys'-'evidence_domains')||jsonb_build_object('weekly_sleep',w.weekly_sleep)||jsonb_build_object('trends',coalesce(t.vals,'{}')) order by b.capture,b.profile)
              from derived.briefing_components_v1 b
              join derived.briefing_signals_v1 s using(profile,capture)
              join derived.workout_hr_zones_v1 z using(profile,capture)
              join derived.weekly_sleep_analysis_v1 w using(profile,capture)
              join derived.daily_decision_boundary_v1 d using(profile,capture)
              left join trends t using(profile,capture)"""))
            sql_quality = json.loads(sql("select json_agg(quality order by capture,profile) from derived.captured_dashboard_sleep_quality_v1"))
            if sql_quality != expected_quality:
                raise AssertionError("captured dashboard sleep quality disagrees with the production Go reader")
            for i,(row, oracle) in enumerate(zip(records,expected,strict=True)):
                if row.pop('profile') != f'profile-{i%2}' or row.pop('capture') != i//2 or row != oracle:
                    raise AssertionError(f'fixture {i}: {[(k,row.get(k),v) for k,v in oracle.items() if row.get(k)!=v]}')
            if os.environ.get("HEALTH_METRIC_AUDIT_DIR"):
                command([PILOT/".venv/bin/python", PILOT/"scripts/capture_bound_metrics.py", str(port), "briefing"])
            print(f'PASS: {len(records)} synthetic captures matched Go across briefing sections, overall status, correlation, stress zones, weekly sleep, decision mode and walking eligibility.')
        finally:
            command(['docker','stop','-t','1',cid],capture_output=True)


if __name__ == '__main__':
    main()
