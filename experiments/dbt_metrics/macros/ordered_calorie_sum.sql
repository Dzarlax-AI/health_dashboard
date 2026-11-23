{% macro ordered_calorie_sum() %}
case when metric_name='active_energy'
     then sum(avg_val order by hour collate "C") filter (where metric_name='active_energy')
     else sum(avg_val) end
{% endmacro %}
