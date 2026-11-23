{% macro daily_step_totals(chosen, ordered_steps=false) %}
select substring(hour,1,10) as day,sum(avg_val::float8{% if ordered_steps %} order by hour collate "C",utc_offset collate "C"{% endif %}) as source_total
from {{ chosen }} group by substring(hour,1,10)
{% endmacro %}
