{% macro daily_step_totals(chosen) %}
select substring(hour,1,10) as day,sum(avg_val::float8) as source_total
from {{ chosen }} group by substring(hour,1,10)
{% endmacro %}
