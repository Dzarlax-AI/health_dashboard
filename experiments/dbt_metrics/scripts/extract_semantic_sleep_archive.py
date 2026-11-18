"""Project two sleep-only NDJSON inputs from a completed local PG17 custom dump."""

import argparse
from collections import Counter
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import tempfile

SEMANTIC = Path(__file__).resolve().parents[1] / "semantic"
SOCKET_PORT = "5433"
TABLES = {
    "health_records": "id bigint primary key, received_at timestamptz, processing_kind text, processing_status text, payload text",
    "metric_points": "health_record_id bigint, metric_name text, units text, date text, qty real, source text, origin text, quality text",
    "completed_night_sleep": "wake_date text primary key, duration_hours float8, source text, source_epoch text, input_hash text, capture_completeness text, duration_assessment text, finalization_state text, claim_eligibility text, covered_interval_start timestamptz, covered_interval_end timestamptz, observed_at timestamptz, algorithm_version text",
    "sleep_period_coverage": "wake_date text primary key, capture_completeness text",
    "completed_sleep_episode": "wake_date text",
    "night_sleep_coverage_commitments": "wake_date text, source text, metric_date text, source_epoch text, capture_completeness text, coverage_generation text, covered_interval_start timestamptz, covered_interval_end timestamptz, observed_at timestamptz, input_hash text",
}
METRICS = {b"sleep_deep", b"sleep_core", b"sleep_rem", b"sleep_awake", b"sleep_unspecified", b"night_sleep_total"}


def run(cmd, *, env, stderr, **kwargs):
    result = subprocess.run(cmd, env=env, stderr=stderr, **kwargs)
    if result.returncode:
        # PostgreSQL COPY errors may contain private input lines.
        raise RuntimeError(f"{Path(cmd[0]).name} failed ({result.returncode})")
    return result.stdout


def write_projection(archive, name, fields, pg, psql, env, errors):
    pg_restore = [pg / "pg_restore", "--data-only", "--schema=health", "--table", name,
                  "--no-owner", "--no-acl", "--file=-", archive]
    count = scanned = 0
    with subprocess.Popen(pg_restore, stdout=subprocess.PIPE, stderr=errors, env=env) as reader:
        header = re.compile(rb"COPY health\." + name.encode() + rb" \(([^)]+)\) FROM stdin;\n")
        for line in reader.stdout:
            match = header.fullmatch(line)
            if match:
                break
        else:
            raise ValueError(f"archive lacks expected COPY data for {name}")
        columns = match[1].split(b", ")
        positions = [columns.index(f.encode()) for f in fields]
        index = {col.decode(): i for i, col in enumerate(columns)}
        # Never execute SQL from the archive. Use compiler-owned columns and
        # table names; pass only individual COPY data lines to a fresh client.
        copy = f"COPY health.{name}({','.join(fields)}) FROM stdin;"
        with subprocess.Popen(psql + ["-c", copy], stdin=subprocess.PIPE, env=env,
                              stdout=subprocess.DEVNULL, stderr=errors) as writer:
            for raw in reader.stdout:
                if raw == b"\\.\n":
                    break
                if not raw.endswith(b"\n"):
                    raise ValueError("truncated archive COPY row")
                scanned += 1
                row = raw[:-1].split(b"\t")
                if len(row) != len(columns):
                    raise ValueError("invalid COPY row width")
                if name == "metric_points":
                    if row[index["metric_name"]] not in METRICS:
                        continue
                    if not b"2026-06-20" <= row[index["date"]][:10] <= b"2026-11-18":
                        continue
                elif name == "health_records":
                    # Keep all IDs and arrival times to reproduce the precise replay
                    # high water and the FK lookup for accepted points. Only matching
                    # JSON documents ever enter the local payload projection trigger.
                    if b'"night_sleep_coverage"' not in row[index["payload"]]:
                        row[index["payload"]] = b"\\N"
                elif not b"2026-06-21" <= row[index["wake_date"]] <= b"2026-11-17":
                    continue
                writer.stdin.write(b"\t".join(row[i] for i in positions) + b"\n")
                count += 1
            else:
                raise ValueError("missing COPY end marker")
            writer.stdin.close()
            if writer.wait():
                raise RuntimeError(f"loading local {name} failed")
        for trailing in reader.stdout:
            if trailing.startswith(b"COPY "):
                raise ValueError(f"unexpected second COPY for {name}")
        if reader.wait():
            raise RuntimeError(f"archive read of {name} failed")
    return {"scanned": scanned, "retained": count}


NARROW_PACKET = """
create function health.trim_payload() returns trigger language plpgsql as $$
declare body jsonb;
begin
  if new.payload is null or new.processing_status not in ('pending','complete') or not (new.payload is json object) then
    new.payload := null;
    return new;
  end if;
  body := new.payload::jsonb -> 'data';
  select jsonb_build_object('data',jsonb_build_object('night_sleep_coverage',s.cs,'metrics',coalesce(s.ms,'[]'::jsonb)))::text
  into new.payload
  from (select
      (select jsonb_agg(c) from jsonb_array_elements(body->'night_sleep_coverage') c
       where c->>'wake_date' between '2026-08-20' and '2026-11-17') as cs,
      (select jsonb_agg(m) from jsonb_array_elements(body->'metrics') m
       where m->>'name'='night_sleep_total') as ms) s where s.cs is not null;
  return new;
end $$;
create trigger trim_payload before insert on health.health_records
for each row when (new.payload is not null) execute function health.trim_payload();
"""


def extract(archive, out, lab, pg, env, errors, as_of):
    psql = [pg / "psql", "-X", "-qAt", "-h", lab, "-p", SOCKET_PORT, "-U", "snapshot_owner",
            "-d", "postgres", "-v", "ON_ERROR_STOP=1"]
    ddl = ["create schema health", "create table health.settings(key text primary key,value text)",
           "insert into health.settings values ('timezone','Europe/Belgrade')"]
    ddl.extend(f"create unlogged table health.{name} ({definition})" for name, definition in TABLES.items())
    run(psql, env=env, stderr=errors, input=(";\n".join(ddl)+";\n"+NARROW_PACKET).encode(),
        stdout=subprocess.DEVNULL)
    counts = {}
    for name, definition in TABLES.items():
        fields = [column.split()[0] for column in definition.split(", ")]
        counts[name] = write_projection(archive,name,fields,pg,psql,env,errors)
    # The data is immutable here; both SQL files may use their own transaction
    # timestamp. The profile's timestamp is the source pseudonym salt.
    guard = b"begin isolation level repeatable read read only; set local search_path=health;\n"
    for filename in ("profile_select", "sleep_replay_select"):
        with (out / (filename+".ndjson")).open("xb") as sink:
            run(psql+["-v","night_card_only=1"], env=env, stderr=errors, stdout=sink,
                input=guard+(SEMANTIC / (filename+".sql")).read_bytes())
    with (out/"profile_select.ndjson").open() as f:
        profile = [json.loads(line) for line in f]
    types = Counter(r["table"] for r in profile)
    nights = {r["row"][0]:r["row"] for r in profile if r["table"]=="completed_night_sleep"}
    metadata = [r["row"] for r in profile if r["table"]=="metadata"]
    if len(metadata)!=1 or metadata[0][1:]!=["Europe/Belgrade","on"] or types["calendar"]!=90:
        raise ValueError("profile metadata or calendar mismatch")
    day = datetime.fromisoformat(metadata[0][0])
    local = day.replace(tzinfo=None).isoformat(sep=" ")
    if "." in local:
        local=local.rstrip("0").rstrip(".")
    offset=day.strftime("%z")
    salt=local+(offset[:3] if offset.endswith("00") else offset[:3]+":"+offset[3:])
    kinds=Counter()
    found=set()
    with (out/"sleep_replay_select.ndjson").open() as f:
        for line in f:
            r=json.loads(line)
            kind=r["kind"]
            kinds[kind]+=1
            if kind=="night":
                key=r["day"]
                hashed=[hashlib.md5((salt+r[field]).encode(),usedforsecurity=False).hexdigest()
                        for field in ("source","epoch")]
                expected=[key,r["duration"],*hashed,r["algorithm"],r["hash"],r["capture"],
                          r["assessment"],r["finality"],r["claim"]]
                if key in found or nights.get(key)!=expected:
                    raise ValueError("canonical night differs between local extracts")
                found.add(key)
            if kind=="meta" and r["read_only"]!="on":
                raise ValueError("missing local read-only replay fence")
    if kinds["meta"]!=1 or not nights or found!=nights.keys() or len(found)!=types["completed_night_sleep"]:
        raise ValueError("local canonical night sets differ")
    if kinds["point"]!=int(run(psql+["-c","select count(*) from health.metric_points where metric_name='night_sleep_total'"],
                               env=env,stderr=errors,stdout=subprocess.PIPE)):
        raise ValueError("accepted point has no matching original record metadata")
    fact_counts=run(psql+["-c","select count(*) from health.health_records where payload is not null and id >= (select coalesce(min(id),0) from health.health_records where received_at >= '2026-08-18'::timestamptz)"],env=env,stderr=errors,stdout=subprocess.PIPE)
    if kinds["packet"]!=int(fact_counts):
        raise ValueError("packet projection count differs")
    manifest={"source":"single primary-tenant custom archive, projected on local PostgreSQL",
              "archive_as_of":as_of, "local_extraction_time":metadata[0][0],
              "evaluation":["2026-08-20","2026-11-17"],"timezone":"Europe/Belgrade","tables":counts,
              "profile_rows":dict(types),"replay_rows":dict(kinds),"reconciled_nights":len(found)}
    (out/"extract-metadata.json").write_text(json.dumps(manifest,indent=2)+"\n")
    print(json.dumps({"profile_rows":dict(types),"replay_rows":dict(kinds),"reconciled_nights":len(found)}))


def main():
    p=argparse.ArgumentParser(description=__doc__)
    p.add_argument("archive", type=Path)
    p.add_argument("output_directory",type=Path,help="new owner-only output directory")
    p.add_argument("--as-of",help="verified archive snapshot timestamp from its metadata")
    args=p.parse_args()
    archive=args.archive.resolve(strict=True)
    if not archive.is_file() or archive.stat().st_mode & (stat.S_IRWXG|stat.S_IRWXO):
        raise ValueError("expected owner-only regular archive")
    with archive.open("rb") as f:
        if f.read(5)!=b"PGDMP":
            raise ValueError("expected PostgreSQL custom archive")
    os.umask(0o077)
    target=args.output_directory.absolute()
    if target.exists():
        raise FileExistsError("output directory must be new")
    env={k:v for k,v in os.environ.items() if not k.startswith("PG") and k!="DATABASE_URL"}
    pg=Path(subprocess.check_output(["pg_config","--bindir"],text=True).strip())
    if not re.search(r"\b17\.\d+", subprocess.check_output([pg/"pg_restore","--version"],text=True)):
        raise RuntimeError("PostgreSQL 17 binaries required")
    # No production connection strings or restored executable objects are used.
    with tempfile.TemporaryDirectory(prefix="sem-archive-",dir="/private/tmp") as scratch:
        lab=Path(scratch)
        out=Path(tempfile.mkdtemp(prefix=".sleep-extract-",dir=target.parent))
        try:
            with (lab/"errors").open("wb") as errors:
                run([pg/"initdb","-D",lab/"db","-U","snapshot_owner","-A","trust","--no-locale","-E","UTF8"],
                    env=env,stderr=errors,stdout=subprocess.DEVNULL)
                run([pg/"pg_ctl","-D",lab/"db","-l",lab/"postgres.log","-o",
                     f"-k {lab} -h '' -p {SOCKET_PORT} -c timezone=Europe/Belgrade", "-w","start"],
                    env=env,stderr=errors,stdout=subprocess.DEVNULL)
                try:
                    extract(archive,out,lab,pg,env,errors,args.as_of)
                finally:
                    run([pg/"pg_ctl","-D",lab/"db","-m","fast","-w","stop"],
                        env=env,stderr=errors,stdout=subprocess.DEVNULL)
            if target.exists():
                raise FileExistsError("output directory appeared during restore")
            out.rename(target)
        finally:
            if out.exists():
                shutil.rmtree(out)

if __name__=="__main__":
    main()
