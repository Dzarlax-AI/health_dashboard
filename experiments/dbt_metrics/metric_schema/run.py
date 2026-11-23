#!/usr/bin/env python3
"""Build and verify a local synthetic metric bundle using actual catalog readers."""
import argparse
import hashlib
from collections import defaultdict
from datetime import datetime
import json
import math
import os
from pathlib import Path
import subprocess
import sys
import tempfile

HERE = Path(__file__).resolve().parent
PILOT = HERE.parent
ROOT = PILOT.parents[1]
sys.path.insert(0, str(PILOT / "scripts"))
from check_semantic_catalog import SWIFT, read_catalog, validate

NON_VALUES = read_catalog()["non_values"]


def number(v):
    return type(v) in (int, float) and math.isfinite(v)


def valid_type(kind, value):
    if value is None:
        return True
    if kind == "timestamp":
        if not isinstance(value, str):
            return False
        try:
            datetime.fromisoformat(value.replace("Z", "+00:00"))
        except ValueError:
            return False
        return True
    if kind in ("number", "duration"):
        return number(value)
    if kind == "integer":
        return type(value) is int
    if kind == "boolean":
        return type(value) is bool
    if kind == "text":
        return isinstance(value, str)
    if kind == "text_list":
        return isinstance(value, list) and all(isinstance(v, str) for v in value)
    if kind in ("number_map", "count_map"):
        return isinstance(value, dict) and all(valid_type("number" if kind == "number_map" else "integer", v)
                                                for v in value.values())
    return kind == "json"


def implementation_fingerprint():
    """Identify this checkout's producers, including local uncommitted formula edits."""
    paths = {ROOT/"go.mod", ROOT/"go.sum", PILOT/"models/health_metric_catalog.yml"}
    paths.update(p for p in PILOT.glob("*.go") if not p.name.endswith("_test.go"))
    for directory in (ROOT/"internal", PILOT/"macros", PILOT/"models"):
        paths.update(p for p in directory.rglob("*") if p.suffix in (".go", ".sql")
                     and not p.name.endswith("_test.go"))
    digest = hashlib.sha256()
    for path in sorted(paths):
        digest.update(path.relative_to(ROOT).as_posix().encode() + b"\0")
        digest.update(hashlib.sha256(path.read_bytes()).digest())
    return digest.hexdigest()


def assemble(paths, *, synthetic=True):
    catalog = read_catalog()
    definitions = {row["metric_id"]: row for row in validate(catalog, SWIFT)}
    implementation = implementation_fingerprint() if synthetic else None
    records = []
    by_metric = defaultdict(set)
    identities = set()
    for path in paths:
        for sample in json.loads(path.read_text()):
            metric_id = sample["metric_id"]
            spec = definitions[metric_id]
            if sample.get("status") == "invalid_path":
                raise ValueError(f"{metric_id}: invalid reader field")
            if sample.get("status") == "gap":
                continue  # Another adapter can supply this field.
            if metric_id in NON_VALUES or "value" not in sample or not sample.get("key") or not sample.get("source"):
                raise ValueError(f"{path.name}/{metric_id}: unexpected or incomplete sample")
            if not valid_type(spec["value_type"], sample["value"]):
                raise ValueError(f"{path.name}/{metric_id}: expected {spec['value_type']}, got {sample['value']!r}")
            key = sample["key"].copy()
            meta = {k:v for k,v in sample.items() if k not in {"metric_id","key","source","profile","value","status"}}
            if synthetic and path.name == "observations.json":
                # Receipt and source snapshot clocks can change on a correction of the same point.
                for field in ("units", "origin", "received_at", "source_snapshot_at"):
                    meta[field] = key.pop(field)
                meta["observed_at"] = key["original_date"]
                meta["available_at"] = meta["received_at"]
            # Each lane has independently generated inputs. Do not join unrelated fixtures into one person.
            profile = path.stem + "/" + sample["profile"] if synthetic else sample["profile"]
            identity = (profile, metric_id, sample["source"], json.dumps(key, sort_keys=True))
            if identity in identities:
                raise ValueError(f"duplicate sample {identity}")
            identities.add(identity)
            if meta.get("version") == "runtime":
                meta.pop("version")
            if synthetic:
                meta["implementation_fingerprint"] = implementation
            records.append(dict(metric_id=metric_id, value_type=spec["value_type"], profile=profile, source=sample["source"], sample_key=key,
                                value=sample["value"], source_metadata=meta))
            by_metric[metric_id].add(sample["source"])
    if synthetic and (gaps := sorted(definitions.keys() - by_metric.keys() - NON_VALUES.keys())):
        raise ValueError("active definitions without executable fixture reads: " + ", ".join(gaps))
    if synthetic:
        for key, binding in catalog["dbt_bindings"].items():
            if binding["model"] not in by_metric[key]:
                raise ValueError(f"{key}: bound dbt model produced no fixture values")
    out = []
    for key, spec in definitions.items():
        disposition, reason = NON_VALUES.get(key, ("active", None))
        if key in NON_VALUES and spec["status"] != disposition:
            raise ValueError(f"{key}: catalog lifecycle drift")
        out.append(dict(metric_id=key, definition=spec, disposition=disposition, reason=reason,
                        read_sources=sorted(by_metric[key]), dbt_binding=catalog["dbt_bindings"].get(key)))
    return dict(schema_version=1, dataset="synthetic" if synthetic else "persisted", implementation_fingerprint=implementation, definitions=out, values=records)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, default=PILOT/"artifacts/unified-metrics.json")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="health-metric-schema-") as tmp:
        lab = Path(tmp)
        env = {k:v for k,v in os.environ.items() if not k.startswith("PG") and k not in
               {"DATABASE_URL", "HEALTH_DB_TESTS", "READINESS_TEST_DSN"}}
        env.update(HEALTH_METRIC_AUDIT_DIR=tmp, PYTHONDONTWRITEBYTECODE="1", DBT_SEND_ANONYMOUS_USAGE_STATS="false")
        for cmd in ([sys.executable, PILOT/"scripts/run_complex_metrics.py"],
                    [sys.executable, PILOT/"scripts/run_pilot.py", "--capture-bindings"],
                    [sys.executable, PILOT/"observations/run.py"],
                    ["go", "run", "./experiments/dbt_metrics/runtimefields"],
                    [sys.executable, PILOT/"namedcontracts/capture_briefing.py"]):
            subprocess.run(cmd, env=env, cwd=ROOT, check=True)
        bundle = assemble(sorted(lab.glob("*.json")))
        verify(bundle)
        args.output.parent.mkdir(parents=True, exist_ok=True)
        with tempfile.NamedTemporaryFile(dir=args.output.parent, mode="w", delete=False) as f:
            try:
                json.dump(bundle, f, ensure_ascii=False, allow_nan=False)
                f.flush()
                os.replace(f.name, args.output)
            finally:
                Path(f.name).unlink(missing_ok=True)
        print(f"{len(bundle['definitions'])} definitions, {len(bundle['values'])} typed synthetic rows. {args.output}")


def verify(bundle):
    # The verifier starts its own PostgreSQL and tests each profile as its restricted role.
    subprocess.run([sys.executable, HERE/"verify_postgres.py"], check=True,
                   input=json.dumps(bundle, ensure_ascii=False, allow_nan=False), text=True)


if __name__ == "__main__":
    main()
