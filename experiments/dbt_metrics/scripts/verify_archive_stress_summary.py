#!/usr/bin/env python3
"""Compare six stress summaries to retained daily values on eligible imputed awake windows.

Select the same dates and hours as verify_stress_hour_archive.py. Return counts
for all eligible days, saved values, and differences at the saved column's type.
No setting or outcome is inferred for earlier decision times.
"""
from jinja2 import Environment, StrictUndefined

from verify_stress_hour_archive import PROJECT, query as hourly_query
from verify_personal_baseline_archive import CHANNEL


def shift(channel):
    """Apply the application's 30-day distribution and same-day median selectors."""
    table,clock,col,predicate=CHANNEL[channel]
    fmt="YYYY-MM-DD" if channel=="hr_overnight" else "YYYY-MM-DD HH24:MI:SS TZHTZM"
    minimum=14 if channel=="temp" else 3
    floor={"temp":0.1,"resp":0.5,"hrv":5.0,"hr_overnight":3.0}[channel]
    source=f"from {table} where {predicate} and {col}>'-Infinity'::float8 and {col}<'Infinity'::float8"
    return f"""left join lateral (
      with samples as materialized (select {col}::float8 v {source}
        and {clock}>=to_char((z.capture-30)::timestamptz,'{fmt}') and {clock}<to_char(z.capture::timestamptz,'{fmt}')),
      center as (select count(*) n,percentile_cont(0.5) within group(order by v) median from samples),
      baseline as (select median,greatest({floor},(select percentile_cont(0.5)
        within group(order by abs(v-median))*1.4826 from samples)) sd from center where n>={minimum}),
      current as (select percentile_cont(0.5) within group(order by {col}::float8) median {source}
        and {clock}>=to_char(z.capture::timestamptz,'{fmt}') and {clock}<to_char((z.capture+1)::timestamptz,'{fmt}'))
      select (current.median-baseline.median)/baseline.sd value from baseline cross join current
    ) {channel} on true"""

def query():
    env=Environment(undefined=StrictUndefined)
    module=env.from_string((PROJECT/"macros/briefing_stress_signals.sql").read_text()).module
    env.globals.update(config=lambda **kwargs:"",var=lambda k,d=None:d,
                       source=lambda _,name:"signal_packets",briefing_stress_signals=module.briefing_stress_signals)
    model=env.from_string((PROJECT/"models/briefing_signals_v1.sql").read_text()).render()
    hourly=hourly_query().split("    select (select count(*) from days)",1)[0]
    return hourly+f""", threshold as (
      select coalesce((select case when pg_input_is_valid(value,'float8') then value::float8 end
        from settings where key='energy.z_threshold'),0.5::float8) as value
    ), day_z as (
      select c.profile,c.capture,jsonb_agg(c.hour_z order by c.hour_start) hours,
        avg(c.hour_z order by c.hour_start) mean_z from candidate c group by c.profile,c.capture
    ), signal_packets as (
      select z.profile,z.capture,jsonb_build_object('hour_z',z.hours,'z_threshold',(select value from threshold),
        'temp',temp.value,'resp',resp.value,'drop',-hrv.value,'rhr',hr_overnight.value,'day_hr',z.mean_z) inputs
      from day_z z
      {" ".join(shift(ch) for ch in ("temp","resp","hrv","hr_overnight"))}
    ), signals as ({model})
    select count(*) as dates,
      count(d.sustained_hr_load) as saved_load,
      count(*) filter(where d.sustained_hr_load is not null and d.sustained_hr_load is distinct from s.load::real) as different_load,
      count(d.stress_flags) as saved_flags,
      count(*) filter(where d.stress_flags is not null and ('acute_stress'=any(d.stress_flags)) is distinct from s.acute) as different_acute,
      count(*) filter(where d.stress_flags is not null and ('sustained_load'=any(d.stress_flags)) is distinct from s.sustained) as different_sustained,
      count(*) filter(where d.stress_flags is not null and ('illness_signature'=any(d.stress_flags)) is distinct from s.illness_signature),
      count(*) filter(where d.stress_flags is not null and ('recovery_debt'=any(d.stress_flags)) is distinct from s.recovery_debt),
      count(*) filter(where d.stress_flags is not null and ('parasympathetic_rebound'=any(d.stress_flags)) is distinct from s.rebound),
      count(*) filter(where s.illness_signature),count(*) filter(where s.recovery_debt),count(*) filter(where s.rebound)
    from signals s join daily_scores d on d.date=s.capture::text"""


if __name__=="__main__":
    print(query().replace(":from_day", ":'from_day'").replace(":until_day", ":'until_day'")+";")
