"""Load a minimized single-transaction extract into an isolated local shadow cluster."""
import hashlib
import json
from bisect import bisect_left, bisect_right
from collections import Counter, defaultdict
from datetime import datetime, timedelta, timezone
from zoneinfo import ZoneInfo


def instant(text):
    return datetime.fromisoformat(text.replace(' +', '+').replace(' -', '-')).astimezone(timezone.utc)


def asleep_seconds(segments):
    total = 0
    covered = None
    for start, end, stage in segments:
        if stage == 'sleep_awake':
            continue
        total += max(0, (end - max(start, covered or start)).total_seconds())
        covered = max(covered or end, end)
    return total


def align_reconstructed_segments(segments, stats):
    """Resolve second-resolution writer boundaries only in duration-derived input."""
    by_start = defaultdict(dict)
    for start, end, stage, whole_seconds in segments:
        if end <= start:
            stats['degenerate_skipped'] += 1
            continue
        # An explicitly fractional copy at the same instant preserves precision.
        observations = by_start[start]
        observations[end, stage] = observations.get((end, stage), True) and whole_seconds
    starts = sorted(by_start)
    result = []
    tolerance = timedelta(seconds=1)
    for i, start in enumerate(starts):
        observations = by_start[start]
        for (end, stage), whole_seconds in sorted(observations.items()):
            if whole_seconds and i + 1 < len(starts) and end != starts[i + 1]:
                first = bisect_left(starts, end - tolerance, lo=i + 1)
                last = bisect_right(starts, end + tolerance, lo=first)
                if first < last:
                    target = by_start[starts[first]]
                    if first != i + 1 or last != first + 1 or len(observations) != 1 or len(target) != 1:
                        stats['ambiguous_unchanged'] += 1
                    elif next(iter(target.values())):
                        stats['overlaps_aligned' if end > starts[first] else 'gaps_aligned'] += 1
                        end = starts[first]
            result.append((start, end, stage))
    return result


def prepare_sleep(data):
    """Reconstruct only a unique longest same-device session matching canonical duration."""
    zone = ZoneInfo('Europe/Belgrade')
    by_source = defaultdict(list)
    summaries = defaultdict(set)
    boundary_counts = Counter()
    for stamp, source, stage, hours in data['segment']:
        start = instant(stamp)
        local = start.astimezone(zone)
        writer_midnight = stamp[11:19] == '00:00:00' and start.microsecond == 0
        if writer_midnight or (local.strftime('%H:%M:%S') == '00:00:00' and local.microsecond == 0):
            # A writer may repeat a midnight summary with an offset variant.
            # Equal recorded quantities represent one observation. Conflicting
            # values retain the presence of the key without picking a winner.
            summary_date = stamp[:10] if writer_midnight else local.date().isoformat()
            key = (summary_date, source, stage)
            summaries[key].add(hours * 60 if hours is not None and 0 <= hours <= 24 else None)
        elif hours is not None and 0 < hours <= 24:
            whole_seconds = start.microsecond == 0 and stamp[19:20] not in ('.', ',')
            by_source[source].append((start, start + timedelta(hours=hours), stage, whole_seconds))
    by_night = defaultdict(list)
    for source, segments in by_source.items():
        group = []
        cursor = None
        for item in align_reconstructed_segments(segments, boundary_counts):
            if cursor and item[0] > cursor + timedelta(minutes=30):
                register_session(group, source, zone, by_night)
                group, cursor = [], None
            group.append(item)
            cursor = max(cursor or item[1], item[1])
        register_session(group, source, zone, by_night)
    rows = []
    reasons = Counter()
    dates = {r[0] for r in data['calendar']}
    for day, duration, source, _, _, _, capture, assessment, *_ in data['completed_night_sleep']:
        if day not in dates:
            continue
        groups = sorted(by_night[day, source], key=lambda g: g[0], reverse=True)
        if capture != 'complete' or assessment != 'plausible':
            reason = 'ineligible_capture_or_duration'
        elif not groups:
            reason = 'no_linked_segments'
        elif len(groups) > 1 and groups[0][0] - groups[1][0] <= 60:
            reason = 'ambiguous_main_session'
        elif abs(groups[0][0] - duration * 3600) > 60:
            reason = 'duration_mismatch'
        else:
            reason = 'matched_session'
            rows.extend((day, source, *s) for s in groups[0][1])
        reasons[reason] += 1
    day_stages = [(*key, next(iter(values)) if len(values) == 1 else None)
                  for key, values in sorted(summaries.items())]
    return rows, day_stages, reasons, boundary_counts


def register_session(group, source, zone, by_night):
    ends = [end for _, end, stage in group if stage != 'sleep_awake']
    if ends:
        wake = max(ends).astimezone(zone).date().isoformat()
        by_night[wake, source].append((asleep_seconds(group), group))


def load_profile(cursor, path, replay=None):
    from psycopg2.extras import execute_values

    data = defaultdict(list)
    with path.open() as f:
        for line in f:
            obj = json.loads(line)
            data[obj['table']].append(obj['row'])
    if len(data['metadata']) != 1 or data['metadata'][0][1:] != ['Europe/Belgrade', 'on']:
        raise ValueError('unsupported snapshot timezone or transaction mode')
    repair = apply_verified_replay(data['completed_night_sleep'], data['metadata'][0][0], *replay) if replay else None
    data['sleep_interval'], data['sleep_stage_day'], link_reasons, boundaries = prepare_sleep(data)
    for table in ('calendar','completed_night_sleep','hrv_point','workout','daily_hrv','sleep_interval','sleep_stage_day'):
        if data[table]:
            execute_values(cursor, f'insert into source_data.{table} values %s', data[table])
    return {
        'replay': repair, 'as_of': data['metadata'][0][0], 'timezone': data['metadata'][0][1],
        'input_counts_including_context': {k: len(v) for k,v in data.items()},
        'periods_all_sleep_intervals_nights_with_capture_window': data['period_counts'][0],
        'episode_linkage_by_day': dict(link_reasons),
        'reconstructed_stage_boundaries': dict(boundaries),
        'inventory': [dict(metric_name=k, units=u, rows=n) for k,u,n in data['inventory']],
    }


def apply_verified_replay(nights, snapshot_time, packet_path, local_result, canonical_path):
    """Join independent snapshots only after reconciling every baseline night."""
    with packet_path.open() as f:
        originals = {}
        for line in f:
            row = json.loads(line)
            if row['kind'] == 'night':
                originals[row['day']] = row
    after = {r['WakeDate']: r for r in json.loads(canonical_path.read_text())}
    result = json.loads(local_result.read_text())
    if {n[0] for n in nights} != originals.keys() or originals.keys() != after.keys():
        raise ValueError('recovery and profile night keys differ')
    applied = {r['Day'] for r in result['details'] if r['Disposition']=='recovered'}
    # PostgreSQL timestamptz::text uses a space and omits an all-zero offset minute.
    stamp = datetime.fromisoformat(snapshot_time)
    local_time = stamp.replace(tzinfo=None).isoformat(sep=' ')
    if '.' in local_time:
        local_time = local_time.rstrip('0').rstrip('.')
    offset = stamp.strftime('%z')
    offset = offset[:3] if offset.endswith('00') else offset[:3]+':'+offset[3:]
    salt = local_time+offset
    change = Counter()
    for n in nights:
        day = n[0]
        old, new = originals[day], after[day]
        before = [old[k] for k in ('algorithm','hash','capture','assessment','finality','claim')]
        identity = [hashlib.md5((salt+old[k]).encode(),usedforsecurity=False).hexdigest()
                    for k in ('source','epoch')]
        if n[2:4] != identity or n[4:] != before or n[1] != old['duration']:
            raise ValueError('canonical input changed between snapshots')
        if new['Source'] != old['source'] or new['SourceEpoch'] != old['epoch']:
            raise ValueError('replay changed the canonical source or epoch')
        updated = [new[k] for k in ('AlgorithmVersion','InputHash','CaptureCompleteness',
                   'DurationAssessment','FinalizationState','ClaimEligibility')]
        if day in applied:
            if new['CaptureCompleteness'] != 'complete':
                raise ValueError('replayed night still incomplete')
            changed_minutes = int(new['DurationHours']*60+0.5) != int(n[1]*60+0.5)
            change['duration_changed' if changed_minutes else 'same_rounded_duration'] += 1
            n[1], n[4:] = new['DurationHours'], updated
        elif n[4:] != updated or n[1] != new['DurationHours']:
            raise ValueError('replay modified a night outside the accepted set')
    return dict(source='local Go InsertAcceptedPoints with bound full packets',packets=result['packets'],
                snapshot=result['meta']['snapshot'],read_only=result['meta']['read_only'],
                before=result['before'],after=result['after'],applied=len(applied),changes=dict(change),
                manual_review=sum(r['Disposition']=='manual_review' for r in result['details']),
                repeated_writes=0)
