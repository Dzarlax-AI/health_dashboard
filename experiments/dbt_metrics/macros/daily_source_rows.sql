{% macro daily_source_rows(hours, scope) %}
select substring(hour,1,10) as date, metric_name, source,
       avg(avg_val) as avg_val, sum(avg_val) as sum_val,
       min(min_val) as min_val, max(max_val) as max_val
from {{ hours }}
where {{ scope }}
group by substring(hour,1,10), metric_name, source
{% endmacro %}
