"""Inspect a single definition or its dated values in a local schema bundle."""
import argparse
import json
from pathlib import Path

DEFAULT = Path(__file__).resolve().parents[1] / "artifacts/unified-metrics.json"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("metric_id")
    parser.add_argument("--bundle", type=Path, default=DEFAULT)
    parser.add_argument("--profile", help="show values for this fixture profile")
    args = parser.parse_args()
    data = json.loads(args.bundle.read_text())
    matches = [row for row in data["definitions"] if row["metric_id"] == args.metric_id]
    if not matches:
        parser.error("unknown metric ID")
    rows = [row for row in data["values"] if row["metric_id"] == args.metric_id]
    profiles = sorted({row["profile"] for row in rows})
    if args.profile:
        if args.profile not in {row["profile"] for row in data["values"]}:
            parser.error("unknown fixture profile")
        result = dict(metric=matches[0], profile=args.profile, values=[r for r in rows if r["profile"] == args.profile])
    else:
        result = dict(metric=matches[0], profiles=profiles, row_count=len(rows))
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
