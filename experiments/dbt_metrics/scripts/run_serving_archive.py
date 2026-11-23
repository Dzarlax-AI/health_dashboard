"""Exercise release migration, tenant worker, and real chart on one trusted local archive."""
from contextlib import contextmanager
import json
import os
from pathlib import Path
import re
import resource
import secrets
import shutil
import subprocess
import tempfile
import time
import uuid

from archive_step_oracle import archive_steps

ROOT = Path(__file__).resolve().parents[3]
PROJECT = ROOT / "experiments/dbt_metrics"
BASE = "863ea99884cdbd4d02f05b1bda9c55af877bfb48"


def require(ok, msg):
    if not ok:
        raise RuntimeError(msg)


def invoke(args, env, *, stdin=None, cwd=ROOT, timeout=1200):
    result = subprocess.run(args, cwd=cwd, env=env, input=stdin, capture_output=True,
                            text=True, timeout=timeout)
    require(result.returncode == 0, f"{Path(args[0]).name} failed ({result.returncode})")
    return result.stdout.strip()


def sql(env, query):
    return invoke(["psql", "-XAtq", "-v", "ON_ERROR_STOP=1"], env, stdin=query)


def prepare(owner):
    require((owner["PGHOST"],owner["PGDATABASE"],owner["PGUSER"]) ==
            ("127.0.0.1","serving_archive","local_serving_owner"), "wrong local archive")
    require(sql(owner,"select current_database()||'/'||current_user") ==
            "serving_archive/local_serving_owner", "unexpected archive owner")
    archived_id = sql(owner,"select tenant_id from health.__tenant_identity where singleton")
    tenant_id = uuid.UUID(archived_id) if archived_id else uuid.uuid4()
    role = "health_t_" + tenant_id.hex
    neighbor_id = uuid.uuid4()
    neighbor_role = "health_t_" + neighbor_id.hex
    neighbor_password = secrets.token_urlsafe(32)
    owner["ARCHIVE_NEIGHBOR_SECRET"] = neighbor_password
    password = secrets.token_urlsafe(32)
    owner["ARCHIVE_ROLE_SECRET"] = password
    # Reassign only relations restored by the temporary importer into this private database.
    sql(owner,rf"""
\getenv tenant_secret ARCHIVE_ROLE_SECRET
\getenv neighbor_secret ARCHIVE_NEIGHBOR_SECRET
create role {role} login password :'tenant_secret' nosuperuser nocreatedb nocreaterole noinherit;
reassign owned by archive_loader to {role};
revoke create on database serving_archive from public,archive_loader,{role};
revoke create on schema public from public;
revoke all on schema health from public;
create role {neighbor_role} login password :'neighbor_secret' nosuperuser nocreatedb nocreaterole noinherit;
create schema archive_neighbor authorization {neighbor_role};
set role {neighbor_role};
create table archive_neighbor.probe(n int primary key);
insert into archive_neighbor.probe values(1);
create table archive_neighbor.__tenant_identity(tenant_id uuid primary key);
insert into archive_neighbor.__tenant_identity values('{neighbor_id}');
reset role;
create table public.serving_archive_guard(tenant_role name not null);
insert into public.serving_archive_guard values('{role}');
revoke all on public.serving_archive_guard from public;
grant select on public.serving_archive_guard to {role};
create schema archive_check;
""")
    scratch = Path(owner["SERVING_ARCHIVE_SCRATCH"]).resolve()
    secret = scratch / "tenant.dsn"
    secret.write_text(f"postgresql://{role}:{password}@127.0.0.1:{owner['PGPORT']}/serving_archive?sslmode=disable")
    secret.chmod(0o400)
    env = {k:owner[k] for k in ("PATH","HOME","TMPDIR","GOCACHE","SERVING_ARCHIVE_SCRATCH") if k in owner}
    env.update(HEALTH_DBT_WORKER_ENABLED="true",HEALTH_DBT_TENANT_ID=str(tenant_id),HEALTH_DBT_TENANT_SCHEMA="health",
               HEALTH_DBT_TENANT_DSN_FILE=str(secret),HEALTH_DBT_BATCH_DAYS="366")
    tenant = dict(env,PGHOST="127.0.0.1",PGPORT=owner["PGPORT"],PGDATABASE="serving_archive",
                  PGUSER=role,PGPASSWORD=password,PGSSLMODE="disable")
    require(sql(tenant,"""select current_user=(select tenant_role from public.serving_archive_guard)
        and not has_database_privilege(current_user,current_database(),'CREATE')
        and not has_schema_privilege(current_user,'archive_neighbor','USAGE')
        and not has_schema_privilege(current_user,'public','CREATE')
        and has_schema_privilege(current_user,'health','CREATE')""")=="t", "isolated tenant role mismatch")
    sql(tenant,"""do $$begin
        begin perform n from archive_neighbor.probe; raise exception 'neighbor readable';
        exception when insufficient_privilege then null; end;
        begin insert into archive_neighbor.probe values(2); raise exception 'neighbor writable';
        exception when insufficient_privilege then null; end;
        begin create schema unexpected_archive_schema; raise exception 'database CREATE enabled';
        exception when insufficient_privilege then null; end;
    end$$;""")
    neighbor = dict(tenant,PGUSER=neighbor_role,PGPASSWORD=neighbor_password)
    require(sql(neighbor,"select (select count(*) from archive_neighbor.probe)=1 and "
                "not has_schema_privilege(current_user,'health','USAGE')")=="t","neighbor boundaries")
    sql(neighbor,"""do $$begin
        begin perform 1 from health.daily_scores limit 1; raise exception 'neighbor read health';
        exception when insufficient_privilege then null; end;
        begin update health.daily_scores set steps=steps where false; raise exception 'neighbor wrote health';
        exception when insufficient_privilege then null; end;
    end$$;""")
    for name in ("report_history","report_delivery_attempts"):
        sql(owner,f"create table archive_check.{name} as select id, md5(to_jsonb(t)::text) fp from health.{name} t")
    return env, tenant


def probe(env, phase, *, root=ROOT):
    args=["go","test","-timeout=20m","-count=1","-v"]
    out = invoke(args+["./experiments/dbt_metrics/servingarchive","-run","^TestServingArchive$"],
                 dict(env,SERVING_ARCHIVE_PHASE=phase),cwd=root)
    require("--- PASS: TestServingArchive" in out,"archive application probe skipped")
    return out


def deployed_probe(env, scratch):
    source = scratch / "v16"
    source.mkdir()
    with tempfile.TemporaryFile() as archive:
        subprocess.run(["git","archive",BASE],cwd=ROOT,stdout=archive,check=True)
        archive.seek(0)
        subprocess.run(["tar","-xf","-","-C",source],stdin=archive,check=True)
    name = Path("experiments/dbt_metrics/servingarchive/archive_test.go")
    (source/name).parent.mkdir()
    (source/name).write_bytes((ROOT/name).read_bytes())
    probe(env,"baseline",root=source)
    probe(env,"chart",root=source)
    (scratch/"chart.json").replace(scratch/"v16-chart.json")
    probe(env,"journal",root=source)
    probe(env,"chart",root=source)
    (scratch/"chart.json").replace(scratch/"v16-same-journal.json")


def worker(env):
    dbt = shutil.which("dbt",path=env["PATH"])
    if not dbt:
        dbt = str(PROJECT/".venv/bin/dbt")
    runtime = Path(dbt).parent/"python"
    require(runtime.is_file(),"install the locked dbt environment or add it to PATH")
    env = dict(env,PATH=str(runtime.parent)+os.pathsep+env["PATH"])
    start=time.monotonic()
    output=invoke([str(runtime),str(PROJECT/"scripts/tenant_aggregate_worker.py"),"--once"],env,timeout=2100)
    return json.loads(output)["status"], round(time.monotonic()-start,3)


def image_once(owner,env,tenant,scratch,image):
    """Run the immutable sidecar on an internal network containing only the disposable database."""
    require(re.fullmatch("sha256:[0-9a-f]{64}",image),"expected an immutable local image ID")
    database_container=owner["SERVING_ARCHIVE_CONTAINER"]
    require(re.fullmatch("[0-9a-f]{64}",database_container),"invalid local database container")
    network="archive-worker-"+secrets.token_hex(8)
    secret=scratch/"image.dsn"
    from urllib.parse import quote
    secret.write_text("postgresql://"+tenant["PGUSER"]+":"+quote(tenant["PGPASSWORD"],safe="")+
                      "@archive-db:5432/serving_archive?sslmode=disable")
    # The containing directory is mode 0700. The production image uses a non-root UID.
    secret.chmod(0o444)
    invoke(["docker","network","create","--internal",network],owner)
    connected=False
    try:
        invoke(["docker","network","connect","--alias","archive-db",network,database_container],owner)
        connected=True
        start=time.monotonic()
        mounted="/run/secrets/archive_tenant"
        result=invoke(["docker","run","--rm","--pull=never","--read-only","--cap-drop=ALL",
            "--security-opt=no-new-privileges","--tmpfs","/tmp:rw,nosuid,nodev,size=512m",
            "--network",network,"--memory=2g","--cpus=2",
            "--mount",f"type=bind,source={secret},target={mounted},readonly",
            "-e","HEALTH_DBT_WORKER_ENABLED=true","-e","HEALTH_DBT_TENANT_SCHEMA=health",
            "-e","HEALTH_DBT_TENANT_ID="+env["HEALTH_DBT_TENANT_ID"],
            "-e","HEALTH_DBT_TENANT_DSN_FILE="+mounted,image,"--once"],owner,timeout=2100)
        return json.loads(result)["status"],round(time.monotonic()-start,3)
    finally:
        if connected:
            invoke(["docker","network","disconnect",network,database_container],owner)
        invoke(["docker","network","rm",network],owner)
        secret.unlink(missing_ok=True)


def meter(owner):
    prefix=["docker","exec",owner["SERVING_ARCHIVE_CONTAINER"]]
    stats=dict(line.split() for line in invoke(prefix+["cat","/sys/fs/cgroup/cpu.stat"],owner).splitlines())
    memory=int(invoke(prefix+["cat","/sys/fs/cgroup/memory.peak"],owner))
    return int(stats["usage_usec"])/1e6,memory


def bootstrap(owner, env, tenant):
    started = time.monotonic()
    sql(tenant,"update health.hdbt_state set shadow_enabled=true")
    db_cpu,_ = meter(owner)
    client = resource.getrusage(resource.RUSAGE_CHILDREN)
    batches=[]
    while True:
        status,elapsed=worker(env)
        require(status in ("published","building"),"worker bootstrap " + status)
        batches.append(elapsed)
        pending=int(sql(owner,"select count(*) from health.hdbt_backfill"))
        print(json.dumps({"bootstrap_batch":len(batches),"pending_dates":pending,"seconds":elapsed}),flush=True)
        if status=="published":
            require(pending==0,"ready before end of inventory")
            break
        require(len(batches)<5000,"bootstrap did not converge")
    cpu_after,memory_peak = meter(owner)
    used = resource.getrusage(resource.RUSAGE_CHILDREN)
    return dict(seconds=round(time.monotonic()-started,3),batches=len(batches),batch_seconds=batches,
                db_cpu_seconds=round(cpu_after-db_cpu,3),container_peak_bytes_since_restore=memory_peak,
                host_child_cpu_seconds=round(used.ru_utime+used.ru_stime-client.ru_utime-client.ru_stime,3))


def chart(env, scratch, route,phase="chart"):
    output=probe(env,phase)
    match=re.search(r"chart_ms=(\d+) chart_shadow=(?:true|false) chart_route=(\w+)",output)
    require(match and match[2]==route,"unexpected chart read path")
    data=json.loads((scratch/"chart.json").read_text())
    pairs=[(p["date"],p["qty"]) for p in data["points"]]
    require(len(pairs)==len(dict(pairs)) and pairs==sorted(pairs),"chart ordering or duplicate date")
    return pairs,int(match[1])


def calorie_comparison(tenant):
    # Both sums consume the same published hourly REAL operands. The ordered sum
    # is the candidate definition and the unordered sum is a deployed-style control.
    return json.loads(sql(tenant,"""with totals as (select substring(hour,1,10) as day,source,
          sum(avg_val order by hour collate "C") as ordered,sum(avg_val) as unordered
          from health.hdbt_hourly_metrics where metric_name='active_energy' group by 1,2),
        chosen as (select day, coalesce(max(ordered) filter(where source like '%Ultra%' or source like '%Apple Watch%'),
           max(ordered) filter(where source like '%iPhone%'),max(ordered)) as ordered,
           coalesce(max(unordered) filter(where source like '%Ultra%' or source like '%Apple Watch%'),
           max(unordered) filter(where source like '%iPhone%'),max(unordered)) as unordered
           from totals group by day)
        select json_build_object('rebuilt_days',count(c.day),'retained_nonnull',count(*) filter(where c.day is null and h.calories is not null),
         'ordered_mismatches',count(*) filter(where h.calories is distinct from coalesce(c.ordered,prior.calories)),
         'unordered_differences',count(*) filter(where c.ordered is distinct from c.unordered),
         'max_ulp',coalesce(max(abs(('x'||encode(float4send(c.ordered),'hex'))::bit(32)::bigint-
            ('x'||encode(float4send(c.unordered),'hex'))::bit(32)::bigint)),0))
         from health.hdbt_daily_metrics h left join chosen c on c.day=h.date left join health.daily_scores prior on prior.date=h.date"""))


def recent_comparison(tenant):
    # Fresh Go-v6 outputs over a dense seven-day range; calories have their own oracle.
    days="""select date from health.daily_scores where date between
        (select (max(date)::date-6)::text from health.daily_scores where steps>0)
        and (select max(date) from health.daily_scores where steps>0)"""
    def count_differences(base,shadow,columns,key):
        def rel(name):
            return f"select {columns} from health.{name} where {key} in ({days})"
        return int(sql(tenant,f"select count(*) from (({rel(base)} except all {rel(shadow)}) union all "
            f"({rel(shadow)} except all {rel(base)})) diff"))
    return {"hour_differences":count_differences("hourly_metrics","hdbt_hourly_metrics",
        "metric_name,hour,source,avg_val,min_val,max_val,sample_count","substring(hour,1,10)"),
        "day_differences_excluding_calories":count_differences("daily_scores","hdbt_daily_metrics",
        "date,hrv_avg,rhr_avg,sleep_total,sleep_deep,sleep_rem,sleep_core,sleep_awake,sleep_unspecified,"
        "steps,exercise_min,spo2_avg,vo2_avg,resp_avg","date")}


def validate(owner, env, tenant, scratch, result):
    days=sql(tenant,"""select substring(date,1,10) as day from health.metric_points
        union select date from health.daily_scores union select substring(hour,1,10) from health.hourly_metrics
        order by 1""").splitlines()
    actual_days=sql(tenant,"select date from health.hdbt_daily_metrics order by date").splitlines()
    require(actual_days==days,"published calendar differs from source inventory")
    reference=archive_steps(lambda q:sql(tenant,q),days)
    # At initial enrollment there is no new withdrawal. Retain a published
    # daily-only value if the archive no longer has any surviving hourly evidence.
    previous = json.loads(sql(tenant,"select coalesce(json_agg(json_build_array(date,steps::float8)),'[]') "
                              "from health.daily_scores where steps is not null"))
    retained = {day:qty for day,qty in previous if day not in reference}
    reference.update(retained)
    result["retained_daily_only"]=len(retained)
    expected=sorted(reference.items())
    result["calendar_days"]=len(days)
    def historical_delta(points):
        observed=dict(points)
        common=reference.keys() & observed.keys()
        return dict(missing=len(reference.keys()-observed.keys()),extra=len(observed.keys()-reference.keys()),
                    different=sum(reference[k]!=observed[k] for k in common),
                    largest_absolute_gap=max((abs(reference[k]-observed[k]) for k in common),default=0))
    legacy={}
    for label in ("v16-chart","v16-same-journal"):
        old=json.loads((scratch/(label+".json")).read_text())
        legacy[label]=historical_delta([(p["date"],p["qty"]) for p in old["points"]])
    ordinary,ms=chart(env,scratch,"go")
    legacy["candidate_flags_off"]=historical_delta(ordinary)
    result["legacy"]=legacy
    result["fallback_all_ms"]=ms
    saved=json.loads(sql(tenant,"select coalesce(json_agg(json_build_array(date,steps::float8) order by date),'[]') "
                         "from health.hdbt_daily_metrics where steps is not null"))
    require([tuple(p) for p in saved]==expected,"shadow values differ from independent oracle before cutover")
    sql(tenant,"update health.hdbt_state set read_enabled=true")
    live,ms=chart(env,scratch,"published")
    require(live==expected,"published chart differs from independent physical-step oracle")
    result["serving_all_ms"]=ms
    chart(env,scratch,"go","range")
    # Both gates explicitly enabled, but this publication is not eligible. Check
    # the actual Go recovery path under both scan-plan configurations.
    sql(tenant,"update health.hdbt_state set ready=false")
    result["raw_all_ms"]={}
    try:
        for plan in ("serial","parallel"):
            raw,latency=chart(dict(env,SERVING_ARCHIVE_PLAN=plan),scratch,"raw")
            require(raw==expected,"canonical Go fallback differs from independent oracle on "+plan)
            result["raw_all_ms"][plan]=latency
    finally:
        sql(tenant,"update health.hdbt_state set ready=true")
    result["chart_days"]=len(live)
    probe(env,"warm")
    recent=recent_comparison(tenant)
    require(not any(recent.values()),f"fresh canonical comparison {recent}")
    result["recent"]=recent
    result["recent_go_v6_calories"]=json.loads(sql(tenant,"""with days as (
        select max(date)::date as hi from health.daily_scores where steps>0)
        select json_build_object('days',count(*),'differing_days',count(*) filter(where go.calories is distinct from v2.calories),
        'max_ulp',coalesce(max(abs(('x'||encode(float4send(go.calories),'hex'))::bit(32)::bigint-
          ('x'||encode(float4send(v2.calories),'hex'))::bit(32)::bigint)),0),
        'different_at_one_decimal',count(*) filter(where round(go.calories::numeric,1) is distinct from round(v2.calories::numeric,1)))
        from health.daily_scores go join health.hdbt_daily_metrics v2 using(date) cross join days
        where go.date between (hi-6)::text and hi::text"""))
    result["calories"]=calorie_comparison(tenant)
    require(result["calories"]["ordered_mismatches"]==0,"ordered calorie oracle mismatch")
    timings=[]
    image=owner.get("HEALTH_ARCHIVE_WORKER_IMAGE")
    for phase in ("first","retry"):
        probe(env,phase)
        # With an outstanding source generation, the same URL must serve Go.
        fallback,dirty_ms=chart(env,scratch,"patch")
        require((fallback==live)==(phase=="retry"),"unexpected corrected step chart")
        status,t=image_once(owner,env,tenant,scratch,image) if image else worker(env)
        require(status=="published","correction was not published")
        published,ms=chart(env,scratch,"published")
        require(published==fallback,"published correction differs from fallback")
        # A real replacement may affect offset aliases recorded on a neighboring
        # date. Rebuild every affected day independently and compare the whole chart.
        received=json.loads((scratch/"accepted.json").read_text())["data"]["metrics"][0]["data"]
        from datetime import date, timedelta
        affected={str(date.fromisoformat(p["date"][:10])+timedelta(days=n))
                  for p in received for n in range(-2,3)}
        updated=archive_steps(lambda q:sql(tenant,q),sorted(affected & set(days)))
        for day in affected:
            reference.pop(day,None)
        reference.update(updated)
        reference.update({d:q for d,q in retained.items() if d in affected and d not in reference})
        require(published==sorted(reference.items()),"corrected chart differs from independent oracle")
        live=published
        require(worker(env)[0]=="idle","extra work after checkpoint")
        receipt=int((scratch/(phase+"-receipt")).read_text())
        lag=float(sql(tenant,f"select extract(epoch from s.published_at-r.received_at) from health.hdbt_state s "
                             f"cross join health.health_records r where s.singleton and r.id={receipt}"))
        require(lag>=0,"invalid receipt-to-checkpoint timing")
        timings.append({"receipt":phase,"worker_seconds":t,"dirty_patch_ms":dirty_ms,"served_ms":ms,"accepted_to_published_seconds":round(lag,3)})
    result["corrections"]=timings
    result["correction_runtime"]="sidecar" if image else "local_locked_dbt"
    sql(tenant,"update health.hdbt_state set read_enabled=false")
    rollback,_=chart(env,scratch,"go")
    # v16 may choose different cached step cells under a different plan. The
    # first rollback selects that legacy route; the issued reports remain frozen.
    result["read_rollback_legacy_differences"]=sum(dict(rollback).get(day)!=qty for day,qty in live)
    sql(tenant,"update health.hdbt_state set shadow_enabled=false")
    require(worker(env)[0]=="disabled","worker did not stop after rollback")
    chart(env,scratch,"go")
    for name in ("report_history","report_delivery_attempts"):
        require(sql(owner,f"""select not exists(select from archive_check.{name} saved full join
            (select id,md5(to_jsonb(t)::text) fp from health.{name} t) cur using(id)
            where saved.fp is distinct from cur.fp)""")=="t","archived delivery changed")
        result[name]=int(sql(owner,f"select count(*) from archive_check.{name}"))
    result["footprint_bytes"]=int(sql(tenant,"""select coalesce(sum(pg_total_relation_size(c.oid)),0)
        from pg_class c join pg_namespace n on c.relnamespace=n.oid where n.nspname='health'
        and left(c.relname,5)='hdbt_' and c.relkind='r'"""))
    result["published_days"]=int(sql(tenant,"select count(*) from health.hdbt_daily_metrics"))
    print(json.dumps(result,sort_keys=True),flush=True)


@contextmanager
def dashboards(env,scratch):
    args=["go","test","-timeout=3h","-count=1","./experiments/dbt_metrics/servingarchive","-run","^TestServingArchive$"]
    p=subprocess.Popen(args,env=dict(env,SERVING_ARCHIVE_PHASE="dashboard"),cwd=ROOT,
                        stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    try:
        end=time.monotonic()+120
        while not (scratch/"dashboard-ready").exists():
            require(p.poll() is None and time.monotonic()<end,"dashboard unavailable")
            time.sleep(.3)
        yield
    finally:
        (scratch/"dashboard-stop").touch()
        require(p.wait(timeout=45)==0,"dashboard probe failed")


def run_candidate(owner,env,tenant,scratch):
    probe(env,"migrate")
    require(sql(tenant,"select schema_contract_version from health.__tenant_identity")=="17","missing v17")
    require(sql(tenant,"select not shadow_enabled and not read_enabled and not ready from health.hdbt_state")=="t",
            "shadow unexpectedly enabled on migration")
    probe(env,"journal")
    require(worker(env)[0]=="disabled","worker enabled before opt-in")
    with dashboards(env,scratch):
        result={"bootstrap":bootstrap(owner,env,tenant)}
        validate(owner,env,tenant,scratch,result)
    result["dashboard"]=json.loads((scratch/"dashboard.json").read_text())
    print(json.dumps(result,sort_keys=True),flush=True)


def main():
    owner=os.environ.copy()
    env,tenant=prepare(owner)
    scratch=Path(owner["SERVING_ARCHIVE_SCRATCH"]).resolve()
    deployed_probe(env,scratch)
    require(sql(tenant,"select schema_contract_version from health.__tenant_identity")=="16","missing baseline v16")
    run_candidate(owner,env,tenant,scratch)


if __name__ == "__main__":
    main()
