{% macro energy_verdict_bands_v1(history) %}
samples as (
  select d.profile, d.capture_id, s.sample, h.day,
         least(100, greatest(0,h.bank)) as bank,
         row_number() over (partition by d.profile,d.capture_id,s.sample,h.day
           order by h.ts_bucket desc,h.computed_at desc,h.formula_version desc,h.bank desc) as rn
  from decisions d cross join (values ('latest'),('compatible')) s(sample)
  join {{ history }} h using(profile,capture_id)
  where h.day::text between (d.through_day-180)::text and d.through_day::text
    and not ('imputed_sleep'=any(h.flags)) and not ('imputed_activity'=any(h.flags))
    and not ('bootstrap_tail'=any(h.flags))
    and (h.formula_version=d.formula_version or
         (s.sample='compatible' and d.formula_version=2 and h.formula_version=1
            and not d.stress_enabled and d.alpha_effective=0.08 and d.beta_effective=0))
    and ((d.alpha_effective=0.08 and d.beta_effective=0) or
         d.settings_changed is null or h.computed_at>=d.settings_changed)
), {{ energy_calibrated_bands_v1() }}
{% endmacro %}
