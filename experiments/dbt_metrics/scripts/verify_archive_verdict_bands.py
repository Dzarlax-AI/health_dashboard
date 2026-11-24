#!/usr/bin/env python3
"""Emit one count-only comparison of retained Energy band history per snapshot day.

The model and application selectors share the calibration macro. Both use current
tenant settings. These are retrospective date cutoffs, not frozen decisions.
"""
from pathlib import Path
from jinja2 import Environment, StrictUndefined

PROJECT = Path(__file__).resolve().parents[1]
COLUMNS = {"source":"source","rest":"rest","recovery":"recovery","push_hard":"push",
           "calibration_mode":"mode","used_days":"used_days","n_data_points":"used_days",
           "latest_formula_days":"latest_days","compatible_formula_days":"compatible_days"}


def query():
    env=Environment(undefined=StrictUndefined)
    env.globals.update(config=lambda **kwargs:"", source=lambda _,name:"input_"+name,
                       var=lambda k,d=None:d)
    for name in ("energy_calibrated_bands_v1","energy_verdict_bands_v1","energy_guidance_v1"):
        env.globals[name]=getattr(env.from_string((PROJECT/"macros"/(name+".sql")).read_text()).module,name)
    model=env.from_string((PROJECT/"models/semantic_energy_guidance_v1.sql").read_text()).render()
    bands=env.globals["energy_verdict_bands_v1"]("history")
    counters=", ".join(f"count(*) filter(where m.{col} is distinct from r.{col})" for col in COLUMNS.values())
    return f"""with kv as (select
        max(value) filter(where key='energy.alpha') a,max(value) filter(where key='energy.alpha_factor') f,
        max(value) filter(where key='energy.beta') b,max(value) filter(where key='energy.stress_drain_enabled') enabled,
        max(value) filter(where key='energy.formula_version') v,
        max(updated_at::timestamptz) filter(where key in ('energy.alpha','energy.alpha_factor','energy.beta','energy.stress_drain_enabled')) changed
        from settings), parsed as (select
        coalesce(case when pg_input_is_valid(a,'float8') then a::float8 end,0.08::float8)*
        coalesce(case when pg_input_is_valid(f,'float8') then f::float8 end,1) as alpha_effective,
        coalesce(case when pg_input_is_valid(b,'float8') then b::float8 end,0.8::float8) as beta,
        coalesce(enabled in ('1','t','T','TRUE','true','True'),false) stress_enabled,
        coalesce(case when v ~ '^[+-]?[0-9]+$' and pg_input_is_valid(v,'int4') then v::int end,3) formula_version,changed
        from kv), cfg as (select *,case when stress_enabled then beta else 0 end as beta_effective from parsed),
    days as (select distinct date::date as day from energy_snapshots),
    input_decisions as (select ''::text profile,day::text capture_id,day through_day,cfg.*,
        case when alpha_effective<>0.08 or beta_effective<>0 then changed end settings_changed,
        null::float8 hrv_z,null::int display_bank,null::text[] flags,null::text confidence,null::text pattern
        from days cross join cfg),
    input_band_history as (select d.profile,d.capture_id,h.date as day,h.ts_bucket,h.computed_at,h.formula_version,h.bank,h.flags
        from input_decisions d join energy_snapshots h on h.date between (d.through_day-180)::text and d.through_day::text),
    candidate as ({model}), reference as (
        select d.capture_id,x.* from input_decisions d cross join lateral (
          with decisions as (select ''::text profile,''::text capture_id,d.through_day,d.formula_version,
              d.alpha_effective,d.beta_effective,d.stress_enabled,d.settings_changed),
          history as (select ''::text profile,''::text capture_id,date as day,ts_bucket,computed_at,formula_version,bank,flags from energy_snapshots),
          {bands}
          select mode,latest_days,compatible_days,used_days,trunc(thresholds[1])::int rest,
            trunc(thresholds[2])::int recovery,trunc(thresholds[3])::int push,
            case when mode='default_warmup' then 'default' else 'personal' end source from bands
        ) x)
    select count(r.capture_id),count(m.capture_id),count(*) filter(where r.capture_id is null or m.capture_id is null),
           count(*) filter(where m.source='personal'),{counters}
    from reference r full join candidate m using(capture_id)"""


if __name__=="__main__":
    print(query()+";")
