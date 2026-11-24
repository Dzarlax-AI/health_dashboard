"""Independent per-date stage, continuity and quality checks on a local archive."""
from collections import defaultdict
from datetime import timedelta
import math
from zoneinfo import ZoneInfoNotFoundError

from semantic_inputs import prepare_sleep


def merge(stages):
    result = []
    for stage, ranges in stages.items():
        union = []
        for start, end in sorted(ranges):
            if union and start <= union[-1][1]:
                union[-1] = (union[-1][0], max(end, union[-1][1]))
            else:
                union.append((start, end))
        result.extend((start, end, stage) for start, end in union)
    return sorted(result)


def calculate(night, spans, summary):
    detail = {"deep", "rem", "core"}
    mins = {}
    awake_count = quality = None
    if not night:
        return "no_night", mins, awake_count, quality
    _, duration, _, _, _, _, capture, assessment, finality, claim = night
    cursor = None
    overlap = significant_overlap = gaps = False
    active = []
    one_second = timedelta(seconds=1)
    for start, end, stage in spans:
        if cursor:
            overlap |= start < cursor
            gaps |= start > cursor
        active = [other for other in active if other[1] > start]
        significant_overlap |= any(stage != other_stage and end - other_start > one_second
                                   and other_end - start > one_second
                                   for other_start, other_end, other_stage in active)
        active.append((start,end,stage))
        cursor = max(cursor or end, end)
        mins[stage] = mins.get(stage, 0) + (end-start).total_seconds()/60
    if significant_overlap:
        status = "overlapping_stage_labels"
    elif detail.intersection(mins):
        status = "recorded_episode_stages"
    elif spans:
        status = "episode_without_detailed_stages"
    elif detail.intersection(summary):
        status = "day_summary_only"
    else:
        status = "no_detailed_stages"
    values = mins if spans else summary
    asleep = sum(v for k, v in values.items() if k != "awake" and v is not None)
    if (capture != "complete" or assessment != "plausible" or not duration or not 0 < duration <= 24
            or overlap or not values or None in values.values() or not asleep or abs(asleep-60*duration) > 1):
        return status, {}, None, None
    if spans and not gaps and spans[0][2] != "awake" and spans[-1][2] != "awake" and detail.intersection(mins):
        awake_count = sum(1 for i, (_, _, k) in enumerate(spans) if k == "awake" and 0 < i < len(spans)-1
                          and spans[i-1][1] == spans[i][0] and spans[i][1] == spans[i+1][0])
    awake = values.get("awake")
    if awake is None and awake_count == 0 and detail <= values.keys():
        awake = 0
    if (values.get("deep") is not None and values.get("rem") is not None and awake is not None
            and finality == "final" and claim == "eligible"):
        duration_factor = min(1., duration/8)
        continuity = duration/(duration+awake/60)
        structure = max(.5, 1-max(0,.15-values["deep"]/60/duration)-max(0,.20-values["rem"]/60/duration))
        quality = int(math.floor(100*duration_factor*continuity*structure+.5))
    return status, values, awake_count, quality


def check_nights(cur, schema, view):
    cur.execute(f"select date from {schema}.daily_scores union select wake_date from {schema}.completed_night_sleep")
    calendar = cur.fetchall()
    cur.execute(f"""select wake_date,duration_hours,source,source_epoch,algorithm_version,input_hash,
        capture_completeness,duration_assessment,finalization_state,claim_eligibility from {schema}.completed_night_sleep""")
    nights = cur.fetchall()
    cur.execute(f"select value from {schema}.settings where key='timezone'")
    zone = cur.fetchone()
    intervals, summaries = [], []
    if zone and zone[0]:
        cur.execute(f"""select date,source,metric_name,qty::float8 from {schema}.metric_points
            where quality='ok' and source<>'' and metric_name in
            ('sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake')
            and lower(units) in ('h','hr','hrs','hour','hours') and date ~ '(Z|[+-][0-9]{{2}}:?[0-9]{{2}})$'
            and pg_input_is_valid(date,'timestamptz')""")
        try:
            intervals, summaries, _, _ = prepare_sleep(dict(calendar=calendar, completed_night_sleep=nights,
                                                             segment=cur.fetchall()), zone[0])
        except ZoneInfoNotFoundError:
            pass
    days = {r[0]: r for r in nights}
    episodes = defaultdict(lambda: defaultdict(list))
    day_stages = defaultdict(dict)
    for date, _, start, end, stage in intervals:
        episodes[date][stage.removeprefix("sleep_")].append((start,end))
    for date, source, stage, minutes in summaries:
        day_stages[date,source][stage.removeprefix("sleep_")] = minutes
    expected = defaultdict(dict)
    for date, in calendar:
        night = days.get(date)
        stage, mins, count, quality = calculate(night, merge(episodes[date]), day_stages[date,night[2]] if night else {})
        expected["main_sleep_episode_stages_v1"][date,None] = stage
        expected["main_sleep_internal_awakenings_v1"][date,None] = count
        expected["main_sleep_night_quality_v1"][date,None] = quality
        for key in ["deep", "rem", "core", "unspecified", "awake"]:
            expected["main_sleep_stage_minutes_v1"][date,key+"_minutes"] = mins.get(key)
    for metric, values in expected.items():
        cur.execute(f"""select sample_key->>'wake_date',sample_key->>'component',value from {view}
            where metric_id=%s and source in ('semantic_sleep_day_v1','semantic_sleep_continuity_v1','semantic_sleep_quality_v1')""", (metric,))
        rows = cur.fetchall()
        actual = {(date,component): value for date,component,value in rows}
        diff = 0
        for key in actual.keys() | values.keys():
            if key not in values or key not in actual:
                diff += 1
            elif isinstance(values[key], float) and isinstance(actual[key], (float,int)):
                diff += not math.isclose(values[key], actual[key], rel_tol=1e-10, abs_tol=1e-6)
            else:
                diff += values[key] != actual[key]
        yield metric, [len(values), len(rows), len(actual), diff]
