{% macro daily_source_mean() %}
avg(avg_val order by source collate "C")::real
{% endmacro %}
