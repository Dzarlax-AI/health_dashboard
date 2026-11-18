"""Aggregate only, including the denominator of all workouts starting in the calendar."""
from collections import Counter, defaultdict
from datetime import date


def summarize(cur):
    def rows(query):
        cur.execute(query)
        return cur.fetchall()

    summary = dict(zip(('from','through','days'), rows(
        'select min(date),max(date),count(*) from source_data.calendar')[0]))
    calendar_span = date.fromisoformat(summary['through']) - date.fromisoformat(summary['from'])
    if summary['days'] != 90 or calendar_span.days != 89:
        raise ValueError('90 evaluation dates required')
    for label, sql in {
        'sleep_status': 'select status,count(*) from semantic_sleep_day_v1 group by 1',
        'stage_status': 'select stage_status,count(*) from semantic_sleep_day_v1 group by 1',
        'norm_day_decisions': 'select reason,count(*) from semantic_sleep_history_v1 group by 1',
        'night_hrv_reasons_by_day': """select reasons,count(*) from (
          select wake_date,string_agg(distinct reason,',' order by reason) reasons
          from semantic_hrv_night_v1 group by 1) n group by 1""",
    }.items():
        summary[label] = dict(rows(sql))
    summary['calendar_counts'] = dict(zip(
        ('sleep_interval_days','hrv_days','confirmed_sdnn_days','night_sdnn_days','both_comparisons_days',
         'valid_hrv_points','sdnn_points','unknown_method_points','fixed_window_days','persisted_daily_days',
         'recomputed_vs_persisted_numeric_pairs','recomputed_vs_persisted_differences','max_persisted_difference_ms'),
        rows("""select count(*) filter(where interval_count>0), count(*) filter(where n_points>0),
          count(*) filter(where sdnn_points>0),count(*) filter(where sources_with_sdnn>0),
          count(*) filter(where sources_with_sdnn>0 and sleep_comparison_status='available'),
          sum(n_points)::int,sum(sdnn_points)::int,sum(unknown_method_points)::int,
          count(*) filter(where fixed_window_points>0),count(persisted_ms),
          count(live_ms-persisted_ms),count(*) filter(where abs(live_ms-persisted_ms)>0.00001),
          max(abs(live_ms-persisted_ms)) from semantic_sleep_hrv_day_v1 s
          join semantic_hrv_daily_v1 h on h.date=s.wake_date""")[0]))
    summary['weighting_differences'] = dict(zip(
        ('paired_days','raw_vs_live_days','max_raw_vs_live_ms','legacy_vs_live_days','max_legacy_vs_live_ms'),
        rows('''select count(raw_ms-live_ms), count(*) filter(where abs(raw_ms-live_ms)>0.00001),
            max(abs(raw_ms-live_ms)), count(*) filter(where abs(legacy_ms-live_ms)>0.00001),
            max(abs(legacy_ms-live_ms)) from semantic_hrv_daily_v1''')[0]))
    for label, view in (('during_workout','semantic_hrv_workout_v1'),('after_workout_30m','semantic_hrv_after_workout_v1')):
        workout_rows = rows(f"""select kind,sum(interval_points),sum(timestamp_points),sum(all_timestamp_candidates),
          sum(unknown_method_points),string_agg(distinct reason,'+' order by reason) from {view} group by id,kind""")
        counts = Counter()
        types = defaultdict(Counter)
        reasons = Counter()
        for kind, measured, timestamp, any_timestamp, unknown, reason in workout_rows:
            for c in (counts, types[kind]):
                c.update(workouts=1, measured_sdnn=int(measured>0), timestamp_sdnn=int(timestamp>0),
                         any_method_timestamp=int(any_timestamp>0), unknown_method=int(unknown>0),
                         measured_points=int(measured), confirmed_timestamp_points=int(timestamp),
                         all_timestamp_candidates=int(any_timestamp))
            reasons['measured_interval' if measured else 'timestamp_only' if timestamp else reason] += 1
        summary[label] = dict(counts=dict(counts),by_kind=dict(types),outcome=dict(reasons))
    summary['channel_time_method'] = [dict(channel=c,time_kind=t,method=m,points=n,devices=s)
        for c,t,m,n,s in rows("""select channel,time_kind,method,count(*),count(distinct source)
        from semantic_hrv_observation_v1 o join source_data.calendar c on c.date=o.writer_date
        group by 1,2,3 order by 1,2,3""")]
    # Anonymous device numbers are scoped to the snapshot; do not export stored source identifiers.
    summary['workout_evidence'] = [dict(period=p,device=int(device),channel=c,method=m,time_kind=t,
        decision=d,window_candidates=n,workouts=w) for p,device,c,m,t,d,n,w in rows("""
         with devices as (select source,row_number() over(order by source) as device
            from (select distinct source from source_data.hrv_point) s)
         select e.period,device,e.channel,e.method,e.time_kind,e.decision,count(*),count(distinct e.id)
         from semantic_hrv_workout_evidence_v1 e join devices using(source)
         join source_data.workout w on e.id=w.id join source_data.calendar c
          on c.date=(w.start_at at time zone 'Europe/Belgrade')::date::text group by 1,2,3,4,5,6 order by 1,2,3,4,5,6""")]
    return summary


def summarize_night_cards(cur):
    """Only aggregate coverage leaves the single-tenant shadow."""
    cur.execute("select min(date),max(date),count(*) from source_data.calendar")
    start, end, days = cur.fetchone()
    if days != 90 or (date.fromisoformat(end) - date.fromisoformat(start)).days != 89:
        raise ValueError('expected 90 contiguous evaluation dates')
    cur.execute("""select c.date, f.card, to_jsonb(e) from source_data.calendar c
                left join semantic_sleep_night_card_v1 f on f.wake_date=c.date
                left join semantic_sleep_continuity_v1 e on e.wake_date=c.date
                order by c.date""")
    rows = cur.fetchall()
    if len(rows) != days or any(not card or card['wake_date'] != day or not episode for day,card,episode in rows):
        raise ValueError('expected a dated night card and a continuity decision for every date')
    coverage, zeros = Counter(), Counter()
    reasons = defaultdict(Counter)
    missing = defaultdict(Counter)
    for _, card, episode in rows:
        quality = card['quality']
        stage_reason = episode['stage_reason']
        continuity_reason = episode['continuity_reason']
        for key, label in [('stage_basis', episode['stage_basis']), ('stages', stage_reason),
                           ('continuity', continuity_reason), ('confidence', quality['confidence']),
                           ('quality_basis',card['quality_basis']),('quality_awake_basis',card['quality_awake_basis']),
                           ('duration', card['duration']['status'])]:
            reasons[key][label] += 1
        for key in ('deep_minutes', 'rem_minutes', 'core_minutes', 'unspecified_minutes', 'awake_minutes',
                    'deep_fraction','rem_fraction','core_fraction','unspecified_fraction','awakening_count','observed_internal_awake_bouts'):
            value = episode[key]
            coverage[key] += value is not None
            zeros[key] += value == 0
            if value is None:
                if key in ('awakening_count', 'observed_internal_awake_bouts'):
                    reason = continuity_reason
                elif stage_reason in ('recorded_episode', 'matched_day_summary'):
                    reason = 'stage_not_observed'
                else:
                    reason = stage_reason
                missing[key][reason] += 1
        for key in ('score_pct','continuity_pct','structure_pct','duration_pct'):
            value = quality.get(key)
            known = value is not None and quality['confidence'] != 'missing'
            coverage[key] += known
            zeros[key] += known and value == 0
            if not known:
                missing[key][card['quality_basis']] += 1
    return {'from': start, 'through': end, 'days': days, 'sleep_status': dict(reasons['duration']),
            'field_coverage': dict(coverage), 'recorded_zero': dict(zeros),
            'decisions': dict(reasons), 'missing_by_field': dict(missing)}
