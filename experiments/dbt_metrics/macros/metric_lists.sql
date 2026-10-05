{% macro sum_metrics() %}
  {{ return([
    'active_energy','apple_exercise_time','apple_stand_hour','apple_stand_time',
    'basal_energy_burned','flights_climbed','nap_total','night_sleep_total',
    'sleep_awake','sleep_core','sleep_deep','sleep_rem','sleep_total',
    'sleep_unspecified','step_count','time_in_daylight','walking_running_distance'
  ]) }}
{% endmacro %}

{% macro sum_metrics_array() -%}
  ARRAY[{% for metric in sum_metrics() %}'{{ metric }}'{% if not loop.last %}, {% endif %}{% endfor %}]::text[]
{%- endmacro %}
