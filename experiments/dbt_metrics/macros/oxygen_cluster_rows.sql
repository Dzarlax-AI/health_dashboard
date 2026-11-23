{% macro oxygen_cluster_rows() %}
with device as (
    select source, count(*) as count, min(qty) as min, avg(qty) as avg,
           count(*) filter (where qty < 94) as below_94_count,
           count(*) filter (where qty < 92) as below_92_count
    from oxygen_samples
    where source is not null and qty > 0 and quality = 'ok'
    group by source
)
select device.*, 'unknown'::text as window,
       sum(count) over ()::bigint as valid, min(min) over () as minimum,
       sum(avg * count) over () / sum(count) over () as mean,
       sum(below_94_count) over ()::bigint as below94,
       sum(below_92_count) over ()::bigint as below92
from device
order by source
{% endmacro %}
