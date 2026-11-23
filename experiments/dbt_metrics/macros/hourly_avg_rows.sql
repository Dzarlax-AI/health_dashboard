{% macro hourly_avg_rows(points) %}
select metric_name, substring(date, 1, 13) || ':00' as hour, source,
       avg(qty order by date collate "C")::real as avg_val, min(qty)::real as min_val,
       max(qty)::real as max_val, count(*)::integer as sample_count
from {{ points }}
where qty > 0 and quality = 'ok'
group by metric_name, substring(date, 1, 13) || ':00', source
{% endmacro %}
