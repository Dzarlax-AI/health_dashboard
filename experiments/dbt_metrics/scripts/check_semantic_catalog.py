"""Verify catalog definitions against the local Go and Health Sync source trees."""
import argparse
import json
import re
from collections import Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parents[3]
CATALOG = ROOT / "experiments/dbt_metrics/semantic/catalog.yml"
SWIFT = ROOT.parent / "health-sync/health-sync/HealthKitManager.swift"


def named_block(source, header):
    return source.split(header, 1)[1].split("\n}", 1)[0]


def observation_evidence(swift_path):
    apple = (ROOT / "internal/applehealth/parse.go").read_text()
    swift = swift_path.read_text()
    ui = (ROOT / "internal/ui/metrics_page.go").read_text()
    policy = (ROOT / "internal/storage/aggregation_policy.go").read_text()
    quality = (ROOT / "internal/health/quality.go").read_text()
    result = {}

    def record(key, field, value):
        result.setdefault(key, {})[field] = value

    for hk, key, unit in re.findall(r'"(\w+)":\s*\{"(\w+)", "(.*?)"\}', named_block(apple, "var hkTypeMap =")):
        record(key, "xml", [hk, unit])
    for hk, key in re.findall(r'"(\w+)":\s*"(\w+)"', named_block(apple, "var sleepValueMap =")):
        result.setdefault(key, {}).setdefault("xml_categories", []).append(hk)
    for key, unit in re.findall(r'MetricName: "(\w+)", Units: "(.*?)"', apple):
        record(key, "xml_special_unit", unit)
    for hk, key, unit, is_sum in re.findall(
        r'\.init\(identifier: \.(\w+),\s*name: "(\w+)",\s*unit: [^\n]+\s*serverUnits: "([^"]+)", isSUM: (true|false)', swift
    ):
        record(key, "ios", [hk, unit, "hour_sum" if is_sum == "true" else "sample_start"])
    for hk, key, kind in re.findall(r'identifierRaw: HKCategoryTypeIdentifier\.(\w+)\.rawValue,\s*name: "(\w+)", kind: \.(\w+)', swift):
        record(key, "ios", [hk, "min" if kind == "durationMinutes" else "count", "event_start"])
    for key, unit in re.findall(r'MetricData\(name: "(\w+)",\s*units: "([^"]+)"', swift):
        if key != "sleep_analysis":
            record(key, "ios", ["sleepAnalysis", unit, "wake_date" if key in {"night_sleep_total", "nap_total"} else "stage_start"])
    declared_ios = set(re.findall(r'\bname: "([a-z0-9_]+)"', swift)) - {"sleep_analysis"}
    if declared_ios != {k for k, v in result.items() if "ios" in v}:
        raise ValueError("unparsed iOS definition: " + str(declared_ios - {k for k,v in result.items() if "ios" in v}))
    for group, keys in re.findall(r'"(\w+)", \[\]string\{([^}]+)\}', ui.split("var categories =", 1)[1].split("\n}\n", 1)[0]):
        for key in re.findall(r'"(\w+)"', keys):
            record(key, "ui_group", group)
    for key in re.findall(r'"(\w+)"', policy.split('metrics := []string{', 1)[1].split('}', 1)[0]):
        record(key, "bucket_sum", True)
    for column, key in re.findall(r'column: "(\w+)", metric: "(\w+)"', policy):
        record(key, "daily_column", column)
    for key, low, high in re.findall(r'"(\w+)":\s*\{Min: ([\d.]+), Max: ([\d.]+)\}', quality):
        record(key, "inclusive_range", [float(low), float(high)])
    if not all(any(field in row for row in result.values()) for field in ("xml","ios","bucket_sum","ui_group","daily_column","inclusive_range")):
        raise ValueError("one or more source registries could not be parsed")
    return result


def go_fields(owner, name):
    block = named_block((ROOT / owner).read_text(), f"type {name} struct {{")
    return set(re.findall(r'^[ \t]*([A-Z]\w*)[ \t]+[^\s/]', block, re.MULTILINE))


def source_units(row):
    units = set()
    if "xml" in row:
        units.add(row["xml"][1])
    if "ios" in row:
        units.add(row["ios"][1])
    if "xml_categories" in row:
        units.add("hr")
    if "xml_special_unit" in row:
        units.add(row["xml_special_unit"])
    return sorted(units)


def unique_pairs(pairs):
    output = {}
    for key, value in pairs:
        if key in output:
            raise ValueError(f"duplicate catalog key: {key}")
        output[key] = value
    return output


def definitions(catalog):
    for key, row in catalog["observations"].items():
        units = source_units(row)
        yield dict(catalog["observation_defaults"], metric_id=key,
                   source_keys=[key], units=units or ["unknown"], evidence=row,
                   quality=row.get("inclusive_range", "no named range; retain source quality"),
                   consumers=["Metrics"] if "ui_group" in row else ["unverified"],
                   method="channel-dependent; see adapter evidence",
                   provenance="named adapter" if units else "producer unconfirmed")
    for family, group in catalog["families"].items():
        shared = {k:v for k,v in group.items() if k not in {"fields","ignored_fields","go_type"}}
        for key, field in group["fields"].items():
            yield catalog["derived_defaults"] | shared | field | {"metric_id": family + "." + key}
    for key, row in catalog["contracts"].items():
        yield catalog["derived_defaults"] | row | {"metric_id":key}


def validate(catalog, swift):
    evidence = observation_evidence(swift)
    rows = catalog["observations"]
    changed = sorted(key for key in rows.keys() | evidence.keys() if rows.get(key) != evidence.get(key))
    if changed:
        raise ValueError("source inventory drift: " + ", ".join(changed))
    for name, family in catalog["families"].items():
        if "go_type" in family:
            actual = go_fields(family["owner"], family["go_type"])
            accounted = {f["field"] for f in family["fields"].values()} | set(family["ignored_fields"])
            if actual != accounted:
                raise ValueError(f"{name}: unmapped/obsolete DTO fields {actual ^ accounted}")
    items = list(definitions(catalog))
    indices = {item["metric_id"]:item for item in items}
    if len(indices) != len(items):
        raise ValueError("duplicate metric id")
    for item in items:
        for prop in ("version","value_type","units","method","grain","clock","parents","quality","coverage","owner","consumers","status","reduction","source_keys"):
            if prop not in item or item[prop] is None or item[prop] == "":
                raise ValueError(f"{item['metric_id']}: missing {prop}")
        if not set(item["units"]).issubset(catalog["units"]):
            raise ValueError(f"undeclared unit in {item['metric_id']}")
        if unknown := set(item["parents"]) - indices.keys():
            raise ValueError(f"{item['metric_id']}: undefined parents {unknown}")
        if not isinstance(item["version"], int) or item["version"] < 1:
            raise ValueError(f"{item['metric_id']}: invalid definition version")
        if not item["units"] or not item["consumers"]:
            raise ValueError(f"{item['metric_id']}: units and consumers require a value or explicit unknown")
        if not (ROOT / item["owner"]).is_file():
            raise ValueError(f"{item['metric_id']}: missing source {item['owner']}")
    active = set()
    visited = set()

    def visit(key):
        if key in active:
            raise ValueError(f"cyclic parent reference from {key}")
        if key not in visited:
            active.add(key)
            for parent in indices[key]["parents"]:
                visit(parent)
            active.remove(key)
            visited.add(key)
    for key in indices:
        visit(key)
    return items


def compare_inventory(catalog, path):
    counts = Counter()
    details = []
    for row in json.loads(path.read_text()):
        key, unit, rows = row["metric_name"], row["units"], row["rows"]
        if type(rows) is not int or rows < 0:
            raise ValueError("inventory requires nonnegative integer row counts")
        known = catalog["observations"].get(key)
        if known is None:
            status = "unclassified"
        elif unit in source_units(known):
            status = "declared_unit"
        else:
            status = "unit_needs_review"
        counts[status] += rows
        details.append(dict(metric_name=key, units=unit, rows=rows, status=status))
    return {"row_counts":dict(counts), "entries":details}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--swift", type=Path, default=SWIFT)
    parser.add_argument("--inventory", type=Path, help="aggregate JSON with metric_name, units, rows only")
    parser.add_argument("--expand", action="store_true", help="emit resolved definitions")
    args = parser.parse_args()
    catalog = json.loads(CATALOG.read_text(), object_pairs_hook=unique_pairs)
    items = validate(catalog, args.swift)
    if args.expand:
        print(json.dumps(items, ensure_ascii=False, indent=2))
    elif args.inventory:
        print(json.dumps(compare_inventory(catalog,args.inventory), ensure_ascii=False))
    else:
        print(f"{len(catalog['observations'])} observations, {len(catalog['families'])} families, "
              f"{len(catalog['contracts'])} named contracts, {len(items)} unique definitions")


if __name__ == "__main__":
    main()
