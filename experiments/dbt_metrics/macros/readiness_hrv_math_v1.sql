{% macro readiness_hrv_math_v1(eligibility) %}
select z.*, z_today*0.60+z_trend*0.40 as blended_z,
       case when available then 0.40::double precision end as component_weight,
       (z_today*0.60+z_trend*0.40)*0.40 as weighted_z
from (
    select *, skip_reason is null as available,
           case when skip_reason is null then
               case when baseline_sd_ms<1e-9 then 0::double precision
                    else (current_ms-baseline_mean_ms)/baseline_sd_ms end end as z_today,
           case when skip_reason is null then
               case when baseline_sd_ms<1e-9 then 0::double precision
                    else (recent7_mean_ms-baseline_mean_ms)/baseline_sd_ms end end as z_trend
    from {{ eligibility }}
) z
{% endmacro %}
