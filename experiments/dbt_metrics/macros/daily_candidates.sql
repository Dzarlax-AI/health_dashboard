{% macro daily_candidates(watch, phone) %}
with aggregate_values as (
    select p.date,p.metric_name,{{ daily_source_mean() }} as source_mean,
           {{ daily_preferred_sum(watch, phone) }} as preferred_total,
           max(sum_val) filter(where g.complete and p.source=g.picked_source) as sleep_value
    from per_source p left join daily_picks g on g.date=p.date
    group by p.date,p.metric_name
)
select d.date,
    max(source_mean) filter(where metric_name='heart_rate_variability') as hrv_avg,
    max(source_mean) filter(where metric_name='resting_heart_rate') as rhr_avg,
    max(sleep_value) filter(where metric_name='sleep_total') as sleep_total,
    max(sleep_value) filter(where metric_name='sleep_deep') as sleep_deep,
    max(sleep_value) filter(where metric_name='sleep_rem') as sleep_rem,
    max(sleep_value) filter(where metric_name='sleep_core') as sleep_core,
    max(sleep_value) filter(where metric_name='sleep_awake') as sleep_awake,
    max(sleep_value) filter(where metric_name='sleep_unspecified') as sleep_unspecified,
    (select source_total from daily_steps where day=d.date) as steps,
    max(preferred_total) filter(where metric_name='active_energy') as calories,
    max(preferred_total) filter(where metric_name='apple_exercise_time') as exercise_min,
    max(source_mean) filter(where metric_name='blood_oxygen_saturation') as spo2_avg,
    max(source_mean) filter(where metric_name='vo2_max') as vo2_avg,
    max(source_mean) filter(where metric_name='respiratory_rate') as resp_avg
from days d left join aggregate_values a using(date)
group by d.date
{% endmacro %}
