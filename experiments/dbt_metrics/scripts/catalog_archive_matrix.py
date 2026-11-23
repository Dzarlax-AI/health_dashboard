"""Combine five anonymized, aggregate-only archive results into a catalog CSV.

--fleet is five JSON objects, one per line in stable profile order (output of
fleet_catalog_audit.py --sql). A proof directory contains profile-1.jsonl
through profile-5.jsonl. Each line is [metric_id, reference_rows, dbt_rows,
count, differences]. For --reader, count is distinct candidate keys. For
--values it is non-null candidate values. Multiple independent proof
directories are allowed. A missing comparison never becomes a successful one.
"""
import argparse
import csv
import json
import sys
from pathlib import Path

from check_semantic_catalog import definitions, read_catalog


def rows(path):
    return [json.loads(line) for line in path.read_text().splitlines() if line.strip()]


def proof_table(paths, kind, known):
    table = [{} for _ in range(5)]
    for path in paths:
        for i, result in enumerate(table, 1):
            seen = set()
            for row in rows(path / f'profile-{i}.jsonl'):
                if kind in ('group', 'recalculation'):
                    metric, reference, candidate, differences = row
                    count = None
                else:
                    metric, reference, candidate, count, differences = row
                if metric not in known or metric in seen:
                    raise ValueError(f'unknown or duplicate proof {metric} in {path.name} profile {i}')
                seen.add(metric)
                counts = (reference, candidate, differences) + (() if count is None else (count,))
                if any(type(n) != int or n < 0 for n in counts) or (count is not None and count > candidate):
                    raise ValueError(f'invalid counts for {metric}')
                if reference != candidate or differences or (kind == 'reader' and count != candidate):
                    verdict = 'different'
                elif not reference:
                    verdict = 'no_rows'
                elif kind == 'value' and not count:
                    verdict = 'only_nulls'
                else:
                    verdict = 'equal'
                summary = '/'.join(map(str, (reference, candidate, differences) if count is None else (reference, candidate, count, differences)))
                result.setdefault(metric, []).append(f'{kind}={verdict}({summary})')
    return table


def source_inventory(path, ids):
    """Report retained inputs separately from numeric comparisons."""
    profiles = [{} for _ in range(5)]
    if path is None:
        return profiles
    for row in rows(path):
        metric = row['metric_id']
        index = row['profile']
        if type(index) is not int or not 1 <= index <= 5 or metric not in ids:
            raise ValueError('unexpected inventory metric or profile')
        counts = (row['retained_reference_rows'], row['complete_input_rows'])
        if any(type(n) is not int or n < 0 for n in counts):
            raise ValueError('invalid inventory counts')
        scope = row['scope']
        if scope not in ('source_inventory', 'method_input_inventory'):
            raise ValueError('unexpected inventory scope')
        result = profiles[index - 1]
        if metric in result:
            raise ValueError('duplicate inventory evidence')
        result[metric] = f'{scope}({counts[0]}/{counts[1]})'
    return profiles


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--fleet', type=Path, required=True)
    parser.add_argument('--reader', type=Path, action='append', default=[])
    parser.add_argument('--values', type=Path, action='append', default=[])
    parser.add_argument('--groups', type=Path, action='append', default=[])
    parser.add_argument('--inventory', type=Path, help='optional private JSONL counts for historical input availability')
    parser.add_argument('--recalculation', type=Path, action='append', default=[],
                        help='independent reconstruction at the current formula version; four fields as in --groups')
    args = parser.parse_args()
    catalog = read_catalog()
    ids = {d['metric_id'] for d in definitions(catalog)}
    fleet = rows(args.fleet)
    if len(fleet) != 5 or not all(r['ready'] for r in fleet):
        raise ValueError('expected five complete, current fleet publications')
    inventory = source_inventory(args.inventory, ids)
    readers = proof_table(args.reader, 'reader', ids)
    values = proof_table(args.values, 'value', ids)
    groups = proof_table(args.groups, 'group', ids)
    rebuilt = proof_table(args.recalculation, 'recalculation', ids)
    out = csv.writer(sys.stdout, lineterminator='\n')
    out.writerow(['metric_id', 'producer', *[f'profile_{i}' for i in range(1,6)]])
    for metric in sorted(ids):
        model = catalog['dbt_bindings'].get(metric, {}).get('model')
        producer = model or ('metric_points' if metric in catalog['observations'] else 'catalog')
        cells = []
        for r, v, g, f, inputs, current in zip(readers, values, groups, fleet, inventory, rebuilt, strict=True):
            results = r.get(metric, []) + v.get(metric, []) + g.get(metric, [])
            if model == 'daily_metrics':
                d = f['daily'][metric]
                difference = d['different'] + d['missing'] + d['extra']
                state = 'different' if difference else ('equal' if d['reference'] else 'no_rows')
                results.append(f"daily={state}({d['reference']}/{d['candidate']}/{difference})")
            results.extend(current.get(metric, []))
            if metric in catalog['non_values']:
                results.append(catalog['non_values'][metric][0])
            if not results:
                results.append('no_archive_comparison')
            if metric in inputs:
                results.append(inputs[metric])
            cells.append(' '.join(results))
        out.writerow([metric, producer, *cells])


if __name__ == '__main__':
    main()
