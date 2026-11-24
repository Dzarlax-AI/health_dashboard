{% macro hourly_stress(hours) %}
select h.*,
       case when not h.coverage_ok or h.coverage_ok is null then null
            when h.hour_hr is null or h.baseline is null or h.sd is null then null
            when h.sd <= 0 or not (h.sd < 'Infinity'::float8 and
                 h.hour_hr > '-Infinity'::float8 and h.hour_hr < 'Infinity'::float8 and
                 h.baseline > '-Infinity'::float8 and h.baseline < 'Infinity'::float8) then 0
            else (h.hour_hr - h.baseline) / h.sd end as hour_z,
       case when h.coverage_ok and h.hour_hr-h.baseline > 0
             and h.hour_hr-h.baseline <> 'NaN'::float8 then h.hour_hr-h.baseline else 0 end as overshoot
from {{ hours }} h
{% endmacro %}
