{% macro personal_baseline_result() %}
select sample_count, age_ns, state, state <> 'cold' as eligible,
       case when state <> 'cold' then median end as baseline_median,
       case when state <> 'cold' then greatest(sd, case channel
           when 'hrv' then 5.0 when 'resp' then 0.5 when 'temp' then 0.1 else 3.0 end) end as mad_sd
from (
    select summary.*,
           case when sample_count < case when channel = 'temp' then 14 else 3 end then 'cold'
                when sample_count < 7 or age_ns is null or age_ns > 1209600000000000 then 'warmup'
                else 'steady' end as state
    from summary
) classified
{% endmacro %}
