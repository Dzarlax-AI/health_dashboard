#!/usr/bin/env python3
"""Maintain published hourly and daily dbt aggregates for one enrolled Health tenant."""

import argparse
from datetime import date
import json
import os
from pathlib import Path
import re
import signal
import ssl
import stat
import subprocess
import sys
import tempfile
import threading
import time
import uuid

import psycopg2
from psycopg2.extensions import parse_dsn

PROJECT = Path(__file__).resolve().parents[1]
FORMULA = "aggregate_serving_v3"
SCHEMA_VERSION = 20
SCHEMA_CHECKSUM = "f28ec572d75d2704ea20f6cc1b70a42285ecb1079904367723746fcc179548b5"
JOURNAL = "_cache_maintenance_v1"
POINTS = "metric_name,date,source,qty,quality,health_record_id,origin,units,id,received_at,source_snapshot_at"
DAILY = (
    "date,hrv_avg,rhr_avg,sleep_total,sleep_deep,sleep_rem,sleep_core,sleep_awake,"
    "sleep_unspecified,steps,calories,exercise_min,spo2_avg,vo2_avg,resp_avg"
)
STEPS = "('step_count','__healthkit_archive_sample:step_count')"
WORK = (
    "hdbt_i_publish,hdbt_i_calendar,hdbt_i_points,hdbt_i_records,hdbt_i_repairs,hdbt_i_daily_withdrawals,"
    "hdbt_w_points,hdbt_w_step,hdbt_w_hourly,hdbt_w_daily"
)
STOP = threading.Event()
active_connection = None


def cancel_on_shutdown(read_fd):
    # CPython can defer the Python signal handler while its main thread waits
    # inside libpq. The low-level signal handler writes this byte immediately.
    os.read(read_fd, 1)
    STOP.set()
    while (conn := active_connection) is not None and not conn.closed:
        try:
            conn.cancel()
        except psycopg2.Error:
            return
        time.sleep(0.1)


class WorkerError(Exception):
    """Safe, fixed diagnostic category for a failed poll."""


def configuration():
    schema = os.environ["HEALTH_DBT_TENANT_SCHEMA"]
    tenant = uuid.UUID(os.environ["HEALTH_DBT_TENANT_ID"])
    if (
        not re.fullmatch(r"[a-z][a-z0-9_]{0,62}", schema)
        or schema.startswith("pg_")
        or schema in ("public", "information_schema", "health_registry")
    ):
        raise WorkerError("configuration")
    path = Path(os.environ["HEALTH_DBT_TENANT_DSN_FILE"])
    if not path.is_absolute() or not path.is_file() or path.stat().st_size > 8192:
        raise WorkerError("secret_file")
    if stat.S_IMODE(path.stat().st_mode) & 0o222 and not (os.statvfs(path).f_flag & os.ST_RDONLY):
        raise WorkerError("writable_secret")
    config = parse_dsn(path.read_text().strip())
    allowed = {"host", "port", "user", "dbname", "password", "sslmode", "sslrootcert", "sslcert", "sslkey"}
    if (
        set(config) - allowed
        or not all(config.get(k) for k in ("host", "user", "dbname", "password"))
        or config["user"] != "health_t_" + tenant.hex
    ):
        raise WorkerError("credential_scope")
    if config.get("sslmode") == "verify-full" and "sslrootcert" not in config:
        # pgx uses the system roots by default; libpq otherwise expects a
        # per-user root.crt. Pin the same trust bundle for capture and dbt.
        roots = ssl.get_default_verify_paths().cafile
        if not roots:
            raise WorkerError("system_ca_unavailable")
        config["sslrootcert"] = roots
    config["port"] = int(config.get("port", "5432"))
    batch = int(os.getenv("HEALTH_DBT_BATCH_DAYS", "16"))
    if not 1 <= batch <= 366:
        raise WorkerError("batch_size")
    return schema, str(tenant), config, batch


def authorize(cur, schema, tenant):
    cur.execute(
        """select exists(select from pg_roles r join pg_namespace n on r.oid=n.nspowner
        join pg_class m on m.relnamespace=n.oid and m.relowner=r.oid and m.relname='__tenant_identity'
        where n.nspname=%s and m.relkind='r' and r.rolname=current_user and session_user=current_user
        and r.rolcanlogin and not (r.rolsuper or r.rolcreatedb or r.rolcreaterole or r.rolbypassrls or r.rolreplication)
        and not has_database_privilege(current_database(),'CREATE')
        and not exists(select from pg_auth_members where member=r.oid)
        and not exists(select from pg_class protected where protected.relnamespace=n.oid
            and (starts_with(protected.relname,'hdbt_') or protected.relname in
                 ('__tenant_identity','metric_points','health_records','daily_scores','hourly_metrics','settings'))
            and protected.relkind in ('r','v','m','p','f')
            and (protected.relowner<>r.oid or protected.relkind<>'r' or exists
                (select from aclexplode(coalesce(protected.relacl,acldefault('r',r.oid))) grant_entry
                 where grant_entry.grantee<>r.oid)))
        and not exists(select from aclexplode(coalesce(n.nspacl,acldefault('n',r.oid))) a where a.grantee<>r.oid)
        and not exists(select from pg_namespace peer join pg_class marker on marker.relnamespace=peer.oid
            where marker.relname='__tenant_identity' and peer.oid<>n.oid and has_schema_privilege(peer.oid,'USAGE')))""",
        (schema,),
    )
    if not cur.fetchone()[0]:
        raise WorkerError("tenant_acl")
    cur.execute(f'set search_path to pg_catalog,"{schema}"')
    cur.execute(
        "select count(*)=1 and bool_and(singleton and tenant_id=%s::uuid and schema_contract_version=%s and schema_contract_checksum=%s) from __tenant_identity",
        (tenant, SCHEMA_VERSION, SCHEMA_CHECKSUM),
    )
    if not cur.fetchone()[0]:
        raise WorkerError("tenant_identity")


def watermark(cur, lock=False):
    stmt = "select value::jsonb from settings where key=%s" + (" for share" if lock else "")
    cur.execute(stmt, (JOURNAL,))
    row = cur.fetchone()
    if row is None:
        return None
    state = row[0]
    number = state.get("generation") if isinstance(state, dict) else None
    if (
        not isinstance(state, dict)
        or state.get("version") != 1
        or type(number) is not int
        or not 0 <= number < 2**64
    ):
        raise WorkerError("journal_format")
    return number


def initialize(cur):
    # Raw source and legacy-only dates both participate. This inventory is made
    # once per enrollment or formula change; normal syncs read only the dirty queue.
    cur.execute(
        """delete from hdbt_backfill;
        insert into hdbt_backfill
        with recursive point_days(day) as (
            (select substring(date,1,10) from metric_points order by 1 limit 1)
            union all select following.day from point_days p cross join lateral
                (select substring(date,1,10) as day from metric_points
                 where substring(date,1,10)>p.day order by 1 limit 1) following
        ), hour_days(day) as (
            (select substring(hour,1,10) from hourly_metrics order by 1 limit 1)
            union all select following.day from hour_days h cross join lateral
                (select substring(hour,1,10) as day from hourly_metrics
                 where substring(hour,1,10)>h.day order by 1 limit 1) following
        )
        select day from point_days union select day from hour_days union select date from daily_scores
          union select date from hdbt_daily_metrics union select date from hdbt_daily_withdrawals;
        update hdbt_state set initialized=true,ready=false,formula_id=%s,source_generation=null,
            bootstrap_cursor='',bootstrap_end=coalesce((select max(date) from hdbt_backfill),''),
            last_status='bootstrap',heartbeat_at=clock_timestamp()""",
        (FORMULA,),
    )


def date_window(days, radius):
    return sorted(
        {
            date.fromordinal(n).isoformat()
            for day in days
            for n in range(
                max(date.min.toordinal(), day.toordinal() - radius),
                min(date.max.toordinal(), day.toordinal() + radius) + 1,
            )
        }
    )


def capture(cur, batch, attempt):
    cur.execute("set transaction isolation level repeatable read")
    generation = watermark(cur)
    cur.execute("""select shadow_enabled,initialized,formula_id,enrollment_epoch,ready,source_generation
                   from hdbt_state where singleton""")
    enabled, initialized, formula, epoch, ready, checkpoint = cur.fetchone()
    if not enabled:
        return "disabled"
    if generation is None:
        return "waiting_journal"
    if checkpoint is not None and generation < checkpoint:
        raise WorkerError("journal_regressed")
    if not initialized or formula != FORMULA:
        initialize(cur)
    cur.execute(
        """select date from (
            (select date,0 as priority from hdbt_dirty order by date limit %s) union all
            (select date,1 as priority from hdbt_backfill order by date limit %s)
        ) pending group by date order by min(priority),date limit %s""",
        (batch, batch, batch),
    )
    core = [r[0] for r in cur.fetchall()]
    if not core:
        if ready and generation == checkpoint:
            cur.execute(
                "update hdbt_state set last_status='idle',heartbeat_at=now() where singleton and heartbeat_at<now()-interval '1 minute'"
            )
            return "idle"
        return generation, epoch, []
    try:
        days = [date.fromisoformat(d) for d in core]
    except ValueError:
        raise WorkerError("unsupported_calendar_date") from None
    neighborhood = date_window(days, 2)
    halo = date_window(days, 4)
    cur.execute(
        """insert into hdbt_work_lease values(true,%s::uuid)
        on conflict(singleton) do update set attempt=excluded.attempt""",
        (attempt,),
    )
    cur.execute("truncate " + WORK)
    cur.execute("insert into hdbt_i_publish select unnest(%s::text[])", (core,))
    cur.execute(
        """insert into hdbt_i_calendar
        select date from hdbt_i_publish union select distinct substring(date,1,10) from metric_points
            where substring(date,1,10)=any(%s) union select date from daily_scores where date=any(%s)
            union select distinct substring(hour,1,10) from hourly_metrics where substring(hour,1,10)=any(%s)
            union select date from hdbt_daily_metrics where date=any(%s)""",
        (neighborhood, neighborhood, neighborhood, neighborhood),
    )
    cur.execute(
        f"""insert into hdbt_i_points select {POINTS} from metric_points
        where substring(date,1,10)=any(%s) and (metric_name in {STEPS} or substring(date,1,10) in
            (select date from hdbt_i_calendar))""",
        (halo,),
    )
    cur.execute(f"""insert into hdbt_w_points select * from hdbt_step_inputs
            where substring(date,1,10) in (select date from hdbt_i_calendar except select date from hdbt_backfill);
        insert into hdbt_w_points select * from hdbt_i_points where substring(date,1,10) not in
            (select date from hdbt_i_calendar);
        insert into hdbt_i_records select id,content_type from health_records where id in
            (select health_record_id from hdbt_i_points union select health_record_id from hdbt_w_points);
        insert into hdbt_i_repairs select point_date,source from hdbt_step_repairs
            where substring(point_date,1,10) in (select date from hdbt_i_calendar);
        insert into hdbt_i_daily_withdrawals select date,metric_name from hdbt_daily_withdrawals
            where date in (select date from hdbt_i_calendar);
        insert into hdbt_w_daily select * from hdbt_daily_metrics
            where date in (select date from hdbt_i_calendar except select date from hdbt_backfill);
        insert into hdbt_w_daily select {DAILY} from daily_scores where date in (select date from hdbt_i_calendar)
            on conflict do nothing;
        insert into hdbt_w_step select * from hdbt_step_state
            where substring(hour,1,10) in (select date from hdbt_i_calendar except select date from hdbt_backfill);
        insert into hdbt_w_step select hour,source,avg_val,min_val,max_val,sample_count,false,null
            from hourly_metrics where metric_name='step_count' and substring(hour,1,10) in
                (select date from hdbt_i_calendar except
                    (select date from hdbt_daily_metrics except select date from hdbt_backfill))
            on conflict do nothing;""")
    return generation, epoch, core


def build(config, schema, dates, attempt, target="aggregates"):
    decisions = target == "decisions"
    variables = dict(
        tenant_aggregate_worker=True,
        hdbt_decision_worker=decisions,
        semantic_source_schema=schema,
        verified_step_withdrawals=True,
        affected_dates=dates,
        tenant_aggregate_attempt=attempt,
    )
    if target == "native_catalog":
        from native_catalog_vars import native_catalog_vars
        variables = native_catalog_vars(schema)
        selectors = ["metric_read_v1"]
    elif decisions:
        selectors = ["worker_energy_decisions", "hdbt_readiness_decisions", "worker_today_decisions"]
    else:
        selectors = ["+daily_metrics"]
    profile = dict(
        config,
        type="postgres",
        schema=schema,
        threads=1,
        search_path=f"{schema},pg_catalog",
        password="{{ env_var('DBT_ENV_SECRET_TENANT_PASSWORD') }}",
    )
    with tempfile.TemporaryDirectory(prefix="health-dbt-") as temp:
        path = Path(temp)
        (path / "profiles.yml").write_text(
            json.dumps({"health_dbt_metrics_pilot": {"target": "tenant", "outputs": {"tenant": profile}}})
        )
        env = {k: os.environ[k] for k in ("PATH", "LD_LIBRARY_PATH", "LANG", "TMPDIR") if k in os.environ}
        env.update(
            HOME=temp,
            DBT_SEND_ANONYMOUS_USAGE_STATS="false",
            DBT_ENV_SECRET_TENANT_PASSWORD=config["password"],
        )
        proc = subprocess.Popen(
            [
                "dbt",
                "run" if target == "native_catalog" else "build",
                "--profiles-dir",
                temp,
                "--project-dir",
                str(PROJECT),
                "--target-path",
                str(path / "target"),
                "--log-path",
                str(path / "logs"),
                "--select",
                *selectors,
                "--vars",
                json.dumps(variables),
            ],
            env=env,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
        try:
            deadline = time.monotonic() + 1800
            while proc.poll() is None:
                if STOP.wait(0.25) or time.monotonic() >= deadline:
                    raise WorkerError("build_interrupted")
            if proc.returncode:
                raise WorkerError("dbt_build")
        finally:
            if proc.poll() is None:
                os.killpg(proc.pid, signal.SIGTERM)
                try:
                    proc.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    os.killpg(proc.pid, signal.SIGKILL)
                    proc.wait()
        results = json.loads((path / "target/run_results.json").read_text())["results"]
        models = {r["unique_id"].split(".")[-1] for r in results if r["status"] == "success"}
        if target == "native_catalog":
            valid = models == {"metric_read_v1"}
        elif decisions:
            valid = models == {"worker_energy_decisions", "hdbt_readiness_decisions", "worker_today_decisions"}
        else:
            valid = models == {"hourly_metrics", "daily_metrics", "step_hour_state", "retained_metric_points"}
            valid = valid and any(r["status"] == "pass" for r in results)
        if not valid:
            raise WorkerError("dbt_receipt")


def publish(cur, generation, epoch, dates):
    if watermark(cur, lock=True) != generation:
        return "stale"
    cur.execute(
        """select shadow_enabled and initialized and formula_id=%s and enrollment_epoch=%s
        from hdbt_state where singleton for update""",
        (FORMULA, epoch),
    )
    if not cur.fetchone()[0]:
        return "stale"
    if dates:
        cur.execute(
            """select exists(select date from hdbt_i_calendar except select date from hdbt_w_daily)"""
        )
        if cur.fetchone()[0]:
            raise WorkerError("incomplete_batch")
        for target, source, key in (
            ("hdbt_hourly_metrics", "hdbt_w_hourly", "substring(hour,1,10)"),
            ("hdbt_daily_metrics", "hdbt_w_daily", "date"),
            ("hdbt_step_state", "hdbt_w_step", "substring(hour,1,10)"),
        ):
            cur.execute(f"""delete from {target} where {key} in (select date from hdbt_i_calendar);
                insert into {target} select * from {source} where {key} in (select date from hdbt_i_calendar)""")
        cur.execute(
            f"""delete from hdbt_step_inputs where substring(date,1,10) in (select date from hdbt_i_calendar);
            insert into hdbt_step_inputs select * from hdbt_i_points where metric_name in {STEPS}
                and substring(date,1,10) in (select date from hdbt_i_calendar);
            delete from hdbt_step_repairs where generation<=%s and substring(point_date,1,10) in (select date from hdbt_i_publish);
            delete from hdbt_dirty where generation<=%s and date in (select date from hdbt_i_publish);
            delete from hdbt_daily_withdrawals where generation<=%s and date in (select date from hdbt_i_publish);
            delete from hdbt_backfill where date in (select date from hdbt_i_publish)""",
            (generation, generation, generation),
        )
    cur.execute("""select not exists(select from hdbt_dirty) and not exists(select from hdbt_backfill)""")
    ready = cur.fetchone()[0]
    cur.execute(
        """update hdbt_state set ready=%s, source_generation=case when %s then %s else source_generation end,
        bootstrap_cursor=greatest(bootstrap_cursor,%s),published_at=clock_timestamp(),heartbeat_at=clock_timestamp(),
        last_status=case when %s then 'published' else 'building' end where singleton""",
        (ready, ready, generation, max(dates, default=""), ready),
    )
    return "published" if ready else "building"


def replay_decisions(conn, config, schema, batch):
    # Decisions are immutable receipts. A late sync can append a new revision of
    # an old date after the aggregate watermark has already been acknowledged.
    # Select missing identities independently so every revision can be replayed.
    attempt = str(uuid.uuid4())
    with conn, conn.cursor() as cur:
        cur.execute("select enrollment_epoch from hdbt_state where singleton and shadow_enabled")
        state = cur.fetchone()
        if not state:
            return
        epoch = state[0]
        cur.execute("""select metric,decision_key,input_hash from hdbt_decision_queue
            where blocked_reason is null order by metric,decision_key,input_hash limit %s""", (batch,))
        keys = cur.fetchall()
        if not keys:
            return
        cur.execute("""insert into hdbt_work_lease values(true,%s::uuid)
            on conflict(singleton) do update set attempt=excluded.attempt""", (attempt,))
        cur.execute("truncate hdbt_i_decisions")
        cur.executemany("insert into hdbt_i_decisions(metric,decision_key,input_hash) values(%s,%s,%s)", keys)
    build(config, schema, [], attempt, target="decisions")
    if STOP.is_set():
        return
    with conn, conn.cursor() as cur:
        cur.execute("""select enrollment_epoch=%s and shadow_enabled from hdbt_state
                       where singleton for update""", (epoch,))
        if not cur.fetchone()[0]:
            return
        # Each receipt is classified separately. Unknown staged identities make
        # the entire selected batch suspect; never publish an unexplained row.
        cur.execute("""with staged as (
                select metric,decision_key,input_hash,formula_version,result from hdbt_w_energy
                union all select metric,decision_key,input_hash,formula_version,result from hdbt_w_readiness
                union all select metric,decision_key,input_hash,formula_version,result from hdbt_w_today
            ), unexpected as (
                select 1 from staged s where not exists (select from hdbt_i_decisions i
                  where (i.metric,i.decision_key,i.input_hash)=(s.metric,s.decision_key,s.input_hash))
            ), classified as (
                select i.metric,i.decision_key,i.input_hash,
                  case when exists(select from unexpected) then 'unexpected_stage'
                    when count(s.metric)=0 then 'missing_stage'
                    when count(s.metric)<>1 then 'duplicate_stage'
                    when bool_or(s.formula_version<>j.formula_version or
                      (case when i.metric='energy_bank' then s.result - 'Trace' else s.result end)
                        is distinct from j.result) then 'decision_parity'
                    when bool_or(r.metric is not null and
                      (r.formula_version<>s.formula_version or r.result is distinct from s.result))
                      then 'result_conflict'
                    else null end reason
                from hdbt_i_decisions i join hdbt_decision_inputs j using(metric,decision_key,input_hash)
                left join staged s using(metric,decision_key,input_hash)
                left join hdbt_decision_results r using(metric,decision_key,input_hash)
                group by i.metric,i.decision_key,i.input_hash
            ) update hdbt_decision_queue q set blocked_reason=c.reason from classified c
              where (q.metric,q.decision_key,q.input_hash)=(c.metric,c.decision_key,c.input_hash)
                and c.reason is not null""")
        cur.execute("""insert into hdbt_decision_results(metric,decision_key,input_hash,formula_version,result)
            select s.metric,s.decision_key,s.input_hash,s.formula_version,s.result from (
              select metric,decision_key,input_hash,formula_version,result from hdbt_w_energy
              union all select metric,decision_key,input_hash,formula_version,result from hdbt_w_readiness
              union all select metric,decision_key,input_hash,formula_version,result from hdbt_w_today
            ) s join hdbt_decision_queue q using(metric,decision_key,input_hash)
              join hdbt_i_decisions i using(metric,decision_key,input_hash)
            where q.blocked_reason is null on conflict do nothing""")
        cur.execute("""delete from hdbt_decision_queue q using hdbt_i_decisions i
            where (q.metric,q.decision_key,q.input_hash)=(i.metric,i.decision_key,i.input_hash)
              and q.blocked_reason is null""")
        cur.execute("select exists(select from hdbt_decision_queue where blocked_reason is null)")
        return "building" if cur.fetchone()[0] else "published"


class DecisionReplay:
    def __init__(self):
        self.retry_after = 0.0

    def run(self, conn, config, schema, batch):
        if time.monotonic() < self.retry_after:
            return None
        try:
            return replay_decisions(conn, config, schema, batch)
        except Exception:
            if STOP.is_set():
                raise
            # The decision cooldown never delays the aggregate loop.
            self.retry_after = time.monotonic() + 10
            return "decision_retry"


def once(schema, tenant, config, batch, decisions, install_native_catalog=False):
    global active_connection
    authorized = False
    conn = psycopg2.connect(
        **config,
        connect_timeout=5,
        application_name="health-dbt-worker",
        options="-c search_path=pg_catalog -c lock_timeout=5s -c statement_timeout=10min",
    )
    active_connection = conn
    try:
        if STOP.is_set():
            return "stopped"
        with conn, conn.cursor() as cur:
            authorize(cur, schema, tenant)
            authorized = True
            cur.execute(
                "select pg_try_advisory_lock(hashtextextended('health.dbt.worker:'||%s,0))", (tenant,)
            )
            if not cur.fetchone()[0]:
                return "busy"
        if install_native_catalog:
            build(config, schema, [], str(uuid.uuid4()), target="native_catalog")
            return "catalog_installed"
        with conn, conn.cursor() as cur:
            attempt = str(uuid.uuid4())
            candidate = capture(cur, batch, attempt)
        if isinstance(candidate, str):
            if candidate in ("idle", "waiting_journal"):
                decision_status = decisions.run(conn, config, schema, batch)
                return decision_status or candidate
            return candidate
        generation, epoch, dates = candidate
        if dates:
            build(config, schema, dates, attempt)
        if STOP.is_set():
            return "stopped"
        with conn, conn.cursor() as cur:
            result = publish(cur, generation, epoch, dates)
            if result == "stale":
                cur.execute(
                    "update hdbt_state set heartbeat_at=now(),last_status='stale' where singleton and shadow_enabled"
                )
        if result in ("published", "building"):
            decision_status = decisions.run(conn, config, schema, batch)
            if result == "published" and decision_status:
                return decision_status
        return result
    except Exception as error:
        if authorized and not STOP.is_set() and not conn.closed:
            try:
                with conn, conn.cursor() as cur:
                    category = str(error) if isinstance(error, WorkerError) else "database_error"
                    cur.execute(
                        "update hdbt_state set heartbeat_at=now(),last_status=%s where singleton and shadow_enabled",
                        (category,),
                    )
            except psycopg2.Error:
                pass
        raise
    finally:
        active_connection = None
        conn.close()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--once", action="store_true")
    parser.add_argument("--install-native-catalog", action="store_true",
                        help="install the tenant-owned catalog view and exit")
    args = parser.parse_args()
    if os.getenv("HEALTH_DBT_WORKER_ENABLED", "").lower() != "true":
        print('{"status":"disabled"}')
        return int(args.install_native_catalog)
    read_fd, write_fd = os.pipe()
    os.set_blocking(write_fd, False)
    signal.set_wakeup_fd(write_fd)
    signal.signal(signal.SIGTERM, lambda *_: STOP.set())
    signal.signal(signal.SIGINT, lambda *_: STOP.set())
    threading.Thread(target=cancel_on_shutdown, args=(read_fd,), daemon=True).start()
    failures = 0
    decisions = DecisionReplay()
    while not STOP.is_set():
        try:
            result = once(*configuration(), decisions, install_native_catalog=args.install_native_catalog)
            failures = 0
        except Exception as error:
            if STOP.is_set():
                return int(args.install_native_catalog)
            result = str(error) if isinstance(error, WorkerError) else "worker_error"
            failures = min(failures + 1, 6)
        print(json.dumps({"status": result, "consecutive_failures": failures}), flush=True)
        if args.install_native_catalog:
            return int(result != "catalog_installed")
        if args.once:
            return int(bool(failures))
        delay = min(300, 5 * 2**failures) if failures else (1 if result == "building" else 10)
        STOP.wait(delay)
    return int(args.install_native_catalog)


if __name__ == "__main__":
    sys.exit(main())
