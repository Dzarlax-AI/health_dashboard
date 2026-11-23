#!/usr/bin/env python3
"""Render a read-only comparison of the saved Sleep Balance windows and current retained inputs.

Run in a transaction scoped to one archive tenant with its normal search_path.
Supply the report timezone resolved by the app (tenant timezone setting then REPORT_TZ,
then UTC). Output contains only per-field counts. An input correction since a saved
decision may change a result, so this checks current replay against saved outcomes.
"""
import argparse
import re
from pathlib import Path
from zoneinfo import ZoneInfo

MODELS = Path(__file__).resolve().parents[1] / "models"
NAMES = ("sleep_balance_period_v1", "sleep_duration_balance_v1", "sleep_balance_history_v1")
PERSISTED = {
    "goal_hours": "GoalHours", "total_hours": "TotalSleepHours", "delta_hours": "DeltaHours",
    "coverage": "CaptureState", "assessment": "DurationAssessment",
}


def model_sql(name):
    body = (MODELS / (name + ".sql")).read_text().split("\n", 1)[1]
    body = re.sub(r"{{ source\('complex_sleep','(balance_[a-z]+)'\) }}", r"\1", body)
    body = re.sub(r"{{ ref\('(sleep_[a-z_0-9]+)'\) }}", r"\1", body)
    if "{{" in body or "{%" in body:
        raise ValueError(f"unresolved model template in {name}")
    return body


def render(zone):
    ZoneInfo(zone)
    zone_literal = "'" + zone.replace("'", "''") + "'"
    inputs = f"""balance_capture as (
        select 1 as profile,wake_date as capture,wake_date::date,{zone_literal}::text as zone
        from sleep_duration_balance_snapshot
    ), balance_goal as (
        select c.profile,c.capture,g.effective_date::date,g.goal_hours as hours
        from balance_capture c join sleep_goal g on g.effective_date::date<=c.wake_date
    ), balance_coverage as (
        select c.profile,c.capture,k.wake_date::date as period_day,k.capture_completeness as state
        from balance_capture c join sleep_period_coverage k on k.wake_date::date between c.wake_date-13 and c.wake_date
    ), balance_episode as (
        select c.profile,c.capture,e.episode_id as id,e.start_at,e.end_at,e.duration_assessment as assessment
        from balance_capture c join completed_sleep_episode e
        on e.start_at<(c.wake_date+time '12:00') at time zone c.zone
        and e.end_at>((c.wake_date-14)+time '12:00') at time zone c.zone
    )"""
    ctes = [inputs] + [f"{n} as ({model_sql(n)})" for n in NAMES]
    # Go stores an empty reason on complete snapshots; the model returns SQL NULL.
    ctes.append("""windows as (
        select a.wake_date k,a2.wake_date::text kb,a.state=a2.state as state,a.confidence=a2.confidence as confidence,
        nullif(a.incomplete_reason,'') is not distinct from a2.incomplete_reason as incomplete_reason,
        a.balance_hours is not distinct from a2.balance_hours as balance_hours,
        a.calculated_through is not distinct from a2.calculated_through as clock_match
        from sleep_duration_balance_snapshot a full join sleep_duration_balance_v1 a2 on a.wake_date=a2.capture
    )""")
    ctes.append("""saved_periods as (
        select wake_date as capture,(p->>'WakeDate')::date as period_day,p
        from sleep_duration_balance_snapshot cross join lateral jsonb_array_elements(periods) p
    )""")
    fields = []
    for column,key in PERSISTED.items():
        typ = "float8" if column.endswith("hours") else "text"
        fields.append(f"(a.p->>'{key}')::{typ} is not distinct from b.{column} as {column}")
    ctes.append("days as (select a.capture k,b.capture kb," + ",".join(fields) +
                " from saved_periods a full join sleep_balance_history_v1 b using(capture,period_day))")
    mapping = {"windows": {"sleep_balance."+c:c for c in ("state","confidence","incomplete_reason","balance_hours")},
               "days": {"sleep_balance_day."+{"total_hours":"total_sleep_hours","coverage":"capture_state",
                        "assessment":"duration_assessment"}.get(c,c):c for c in PERSISTED}}
    mapping["windows"]["clock_alignment"] = "clock_match"
    queries = []
    for rel,fields in mapping.items():
        for name,column in fields.items():
            queries.append(f"select '{name}' metric,count(k) saved,count(kb) replayed,"
                           f"count(*) filter(where k is null or kb is null or {column} is not true) differences from {rel}")
    return "with " + ",\n".join(ctes) + "\n" + "\nunion all\n".join(queries)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--timezone", required=True)
    print(render(parser.parse_args().timezone) + ";")
