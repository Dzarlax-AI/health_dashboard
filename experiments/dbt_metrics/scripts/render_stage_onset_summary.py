"""Render the dbt study as an aggregate-only psql query for a read-only tenant session."""

import argparse
from pathlib import Path


PILOT = Path(__file__).resolve().parents[1]


def model(name):
    return (PILOT / "models" / f"{name}.sql").read_text()


def substitute(sql, token, value):
    if sql.count(token) != 1:
        raise ValueError(f"expected exactly one template token {token}")
    return sql.replace(token, value)


def pair_onset(sql):
    macro = (PILOT / "macros/pair_research_onset.sql").read_text().strip()
    start, end = "{% macro pair_research_onset() %}", "{% endmacro %}"
    if not macro.startswith(start) or not macro.endswith(end):
        raise ValueError("unrecognized onset pairing macro")
    return substitute(sql, "{{ pair_research_onset() }}", macro[len(start):-len(end)])


def stage_rows_query():
    return pair_onset(model("research_stage_onset_rows_v1"))


def packet_rows_query():
    return substitute(pair_onset(model("research_packet_onset_rows_v1")),
                      "{{ ref('research_packet_onset_base_v1') }}",
                      "(" + model("research_packet_onset_base_v1") + ") AS packet_base")


def summary_query(packet=False):
    prefix = "packet" if packet else "stage"
    summary = model(f"research_{prefix}_onset_summary_v1")
    summary = substitute(summary, "{{ ref('research_stage_onset_rows_v1') }}", "stage_rows")
    inputs = f"stage_rows AS ({stage_rows_query()})"
    if packet:
        inputs += f", packet_rows AS ({packet_rows_query()})"
        summary = substitute(summary, "{{ ref('research_packet_onset_rows_v1') }}", "packet_rows")
    return f"WITH {inputs} SELECT summary FROM ({summary}) AS study_summary"


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--packet", action="store_true", help="compare selected snapshot and raw packet methods")
    args = parser.parse_args()
    if args.packet:
        print(r"""\set ON_ERROR_STOP on
SELECT 1 / ((:'end_date'::date - :'start_date'::date BETWEEN 0 AND 365) IS TRUE)::int AS annual_guard \gset""")
    print(r"""\set ON_ERROR_STOP on
SELECT 1 / ((
    current_setting('transaction_read_only') = 'on'
    AND current_setting('transaction_isolation') = 'repeatable read'
    AND session_user = current_user
    AND current_user = (SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname=current_schema())
    AND :'start_date'::date <= :'end_date'::date
    AND EXISTS (SELECT FROM pg_timezone_names WHERE name=:'tz')
) IS TRUE)::int AS study_guard \gset
WITH research_parameters AS (
    SELECT :'start_date'::date AS start_date, :'end_date'::date AS end_date, :'tz'::text AS tz
)
SELECT summary FROM (""")
    print(summary_query(packet=args.packet))
    print(") AS result;")
