"""Independent physical-step reduction for local archive acceptance. No production SQL is imported."""
import collections
import datetime as dt
import json
import re
import struct

F = struct.Struct("f")
OFFSET = re.compile(r"[+-](?:[01]\d|2[0-3])[0-5]\d\Z")
TIMESTAMP = re.compile(r"\d{4}-\d{2}-\d{2} (?:[01]\d|2[0-3]):[0-5]\d:[0-5]\d [+-](?:[01]\d|2[0-3])[0-5]\d\Z")
Point = collections.namedtuple("Point", "date source qty quality key grain")
Wall = collections.namedtuple("Wall", "qty lo hi n")


def real(value):
    return F.unpack(F.pack(value))[0]


def float_sum(values, *, single=False):
    total = 0.0
    for value in values:
        total = real(total + value) if single else total + value
    return total


def usable(p):
    return p.quality == "ok" and p.qty is not None and (p.qty > 0 or (p.qty == 0 and p.grain == "hour"))


def alias_losers(points):
    equivalents = collections.defaultdict(set)
    for p in points:
        if usable(p) and TIMESTAMP.fullmatch(p.date):
            try:
                instant = dt.datetime.fromisoformat(p.date[:19] + p.date[20:23] + ":" + p.date[23:25])
            except ValueError:
                continue
            equivalents[p.source, p.grain, p.qty, instant].add(p.date)
    losing = set()
    for (source, grain, qty, _), dates in equivalents.items():
        preferred = min(dates, key=lambda d: (d[21:25] == "0000", d))
        losing.update((date, source, grain) for date in dates if date != preferred)
    return losing


def minute_bounds(points):
    minutes = {}
    for p in points:
        if p.qty is None or p.qty <= 0:
            continue
        key = p.date[:16]
        old = minutes.get(key)
        minutes[key] = (min(old[0], p.qty), max(old[1], p.qty)) if old else (p.qty, p.qty)
    return [v for _, v in sorted(minutes.items())]


def wall_summary(bounds):
    if not bounds:
        return None
    return Wall(float_sum((hi for _, hi in bounds), single=True), min(x[0] for x in bounds),
                max(x[1] for x in bounds), len(bounds))


def wall_row(points):
    positives = wall_summary(minute_bounds(p for p in points if p.quality == "ok"))
    if positives is not None:
        return positives
    if any(p.grain == "hour" and p.quality == "ok" and p.qty == 0 for p in points):
        return Wall(0.0, 0.0, 0.0, 1)
    return None


def proven(points, counted, stored):
    if stored.qty == 0 and any(p.qty == 0 and p.grain == "hour" for p in points):
        return True
    forms = [points]
    if len(counted) < len(points):
        forms.append(counted)
    # The stored wall row can predate quality filtering or the second raw storage key.
    for base in forms:
        for rows in (base, [p for p in base if p.key == "step_count"]):
            for evidence in (rows, [p for p in rows if p.quality == "ok"]):
                bounds = minute_bounds(evidence)
                if len(bounds) < stored.n:
                    continue
                for i in range(len(bounds) - stored.n + 1):
                    if wall_summary(bounds[i:i + stored.n]) == stored:
                        return True
    return False


def priority(candidates):
    return min(candidates, key=lambda x: (not (x[1] > 0),
               1 if "Ultra" in x[0] or "Apple Watch" in x[0] else 2 if "iPhone" in x[0] else 3,
               -x[1], x[0]))[1]


def physical_hours(sources, cache, losers):
    counted = {s: [p for p in points if (p.date, s, p.grain) not in losers]
               for s, points in sources.items()}
    ambiguous = any(usable(p) and not OFFSET.fullmatch(p.date[20:])
                    for points in sources.values() for p in points)
    if cache and (ambiguous or any(not proven(sources.get(s, []), counted.get(s, []), cell)
                                   for s, cell in cache.items())):
        held = []
        for source, old in cache.items():
            before, after = sources.get(source, []), counted.get(source, [])
            cell = old
            if len(after) != len(before) and wall_row(before) == old:
                cell = wall_row(after)
            if cell is not None:
                held.append((source, cell.qty))
        if held:
            return [("", priority(held))]
    if ambiguous:
        candidates = [(s, row.qty) for s, points in counted.items() if (row := wall_row(points)) is not None]
        return [("", priority(candidates))] if candidates else []

    by_offset = collections.defaultdict(list)
    for source, points in counted.items():
        groups = collections.defaultdict(lambda: collections.defaultdict(lambda: collections.defaultdict(list)))
        for p in points:
            if usable(p):
                groups[p.date[20:25]][p.grain][p.date[:16]].append((p.date, p.qty))
        for offset, grains in groups.items():
            grain = next(g for g in ("hour", "legacy", "archive") if g in grains)
            amounts = []
            for _, values in sorted(grains[grain].items()):
                ordered = [v for _, v in sorted(values)]
                amounts.append(max(ordered) if grain == "legacy" else float_sum(ordered))
            by_offset[offset].append((source, float_sum(amounts)))
    return [(offset, priority(candidates)) for offset, candidates in sorted(by_offset.items())]


def archive_steps(query, days):
    """Read bounded batches including two calendar days of context for cross-date aliases."""
    out = {}
    for i in range(0, len(days), 40):
        batch = days[i:i + 40]
        start, end = dt.date.fromisoformat(batch[0]), dt.date.fromisoformat(batch[-1])
        before, after = start - dt.timedelta(days=2), end + dt.timedelta(days=2)
        rows = json.loads(query(f"""
          select coalesce(json_agg(json_build_array(t.date,t.source,t.qty::float8,t.quality,t.metric_name,t.grain) order by t.clock,(t.origin='live'),t.id),'[]')
          from (select p.date,coalesce(p.source,'') as source,p.qty,p.quality,p.metric_name,
                 case when p.origin='apple_health_xml' or r.content_type in ('apple-health-import','apple-health-import; catalog=1')
                      then 'archive' when r.content_type='application/json; metrics-contract=2' then 'hour' else 'legacy' end as grain,
                 coalesce(p.source_snapshot_at,case when p.origin<>'apple_health_xml' then p.received_at end,'-infinity'::timestamptz) clock,
                 p.origin,p.id from health.metric_points p left join health.health_records r on r.id=p.health_record_id
                 where p.metric_name in ('step_count','__healthkit_archive_sample:step_count') and p.units='count'
                   and substring(p.date,1,10) between '{before}' and '{after}') t"""))
        latest = {}
        for row in rows:
            point = Point(*row)
            latest[point.date, point.source, point.grain] = point
        losers = alias_losers(latest.values())
        hours = collections.defaultdict(lambda: collections.defaultdict(list))
        wanted = set(batch)
        for point in latest.values():
            if point.date[:10] in wanted:
                hours[point.date[:13]][point.source].append(point)
        cache = collections.defaultdict(dict)
        for hour, source, qty, lo, hi, n in json.loads(query(f"""
            select coalesce(json_agg(json_build_array(hour,source,avg_val::float8,min_val::float8,max_val::float8,sample_count)),'[]')
            from health.hourly_metrics where metric_name='step_count' and substring(hour,1,10) between '{start}' and '{end}'""")):
            if hour[:10] in wanted:
                cache[hour[:13]][source] = Wall(qty, lo, hi, n)
        totals = collections.defaultdict(list)
        for hour in sorted(hours.keys() | cache.keys()):
            for _, quantity in physical_hours(hours[hour], cache[hour], losers):
                totals[hour[:10]].append(quantity)
        out.update((day, real(float_sum(values))) for day, values in totals.items())
    return out
