"""Prepare a disposable isolated archive's canonical-night model inputs.

Only canonical fields and explicit missing calendar dates are compared. Source
intervals are linked only when the recorded profile timezone is available.
"""
import os
import re

import psycopg2
from psycopg2.extras import execute_values

from semantic_inputs import prepare_sleep


def main():
    name = os.environ["ARCHIVE_DB"]
    if not re.fullmatch("retained_[0-4]", name):
        raise ValueError("requires disposable archive")
    with psycopg2.connect(host="127.0.0.1", user="postgres", dbname=name) as db, db.cursor() as c:
        c.execute("select exists(select from public.architecture_fixture where token=%s)",
                  (os.environ["ARCHIVE_FIXTURE_TOKEN"],))
        if not c.fetchone()[0]:
            raise ValueError("archive fixture identity differs")
        c.execute("""create unique index on source_data.completed_night_sleep(wake_date);
            create table source_data.calendar as select date from source_data.daily_scores
              union select wake_date from source_data.completed_night_sleep;
            create table source_data.sleep_interval(wake_date text,source text,start_at timestamptz,end_at timestamptz,stage text);
            create table source_data.sleep_stage_day(date text,source text,metric_name text,observed_minutes float8);""")
        c.execute("select value from source_data.settings where key='timezone'")
        row = c.fetchone()
        if not row or not row[0]:
            return
        c.execute("select date from source_data.calendar")
        calendar = c.fetchall()
        c.execute("""select wake_date,duration_hours,source,source_epoch,algorithm_version,input_hash,
                        capture_completeness,duration_assessment from source_data.completed_night_sleep""")
        nights = c.fetchall()
        c.execute("""select date,source,metric_name,qty::float8 from source_data.metric_points
            where metric_name in ('sleep_deep','sleep_core','sleep_rem','sleep_awake','sleep_unspecified')
              and source is not null and quality='ok' and date ~ '[-+]([0-9]{2}:?[0-9]{2})$'""")
        intervals, stages, _, _ = prepare_sleep(dict(calendar=calendar,completed_night_sleep=nights,
                                                  segment=c.fetchall()),row[0])
        execute_values(c,"insert into source_data.sleep_interval values %s",intervals)
        execute_values(c,"insert into source_data.sleep_stage_day values %s",stages)


if __name__ == "__main__":
    main()
