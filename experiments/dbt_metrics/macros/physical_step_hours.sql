{% macro physical_step_hours(points, cached, ordered_steps=false) %}
select distinct on(hour,utc_offset) hour,utc_offset,source,avg_val,min_val,max_val
from ({{ step_source_candidates(points,cached, ordered_steps) }}) candidates order by hour,utc_offset,{{ step_source_order('source','avg_val') }}
{% endmacro %}
