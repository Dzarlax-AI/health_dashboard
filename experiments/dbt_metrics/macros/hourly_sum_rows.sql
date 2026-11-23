{% macro hourly_sum_rows(points) %}
select metric_name, hour, source, sum(minute_max)::real as avg_val,
       min(minute_min)::real as min_val, max(minute_max)::real as max_val,
       count(*)::integer as sample_count
from (
    select metric_name, source, substring(date, 1, 13) || ':00' as hour,
           max(qty)::real as minute_max, min(qty)::real as minute_min
    from {{ points }}
    where qty > 0 and quality = 'ok'
    group by metric_name, source, substring(date, 1, 13) || ':00', substring(date, 1, 16)
) minutes
group by metric_name, hour, source
{% endmacro %}
