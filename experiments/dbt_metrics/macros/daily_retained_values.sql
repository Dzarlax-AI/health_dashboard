{% macro daily_retained_values(fresh, prior, step_withdrawn) %}
coalesce({{ fresh }}.hrv_avg,{{ prior }}.hrv_avg)::real as hrv_avg,
coalesce({{ fresh }}.rhr_avg,{{ prior }}.rhr_avg)::real as rhr_avg,
{{ daily_retained_sleep(fresh, prior) }},
coalesce({{ fresh }}.steps, case when not ({{ step_withdrawn }}) then {{ prior }}.steps end)::real as steps,
coalesce({{ fresh }}.calories,{{ prior }}.calories)::real as calories,
coalesce({{ fresh }}.exercise_min,{{ prior }}.exercise_min)::real as exercise_min,
coalesce({{ fresh }}.spo2_avg,{{ prior }}.spo2_avg)::real as spo2_avg,
coalesce({{ fresh }}.vo2_avg,{{ prior }}.vo2_avg)::real as vo2_avg,
coalesce({{ fresh }}.resp_avg,{{ prior }}.resp_avg)::real as resp_avg
{% endmacro %}
