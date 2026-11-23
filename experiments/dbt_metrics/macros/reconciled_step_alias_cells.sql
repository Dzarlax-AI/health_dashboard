{% macro reconciled_step_alias_cells(points,cached, ordered_steps=false) %}
with changed_cells as (
    select distinct substring(date,1,13)||':00' as hour,source from {{ points }} where not counted
), cell_points as (
    select 'step_count'::text as metric_name,p.date,p.source,p.qty,p.quality,p.grain,p.counted
    from {{ points }} p join changed_cells c on (substring(p.date,1,13)||':00',p.source)=(c.hour,c.source)
), original_points as (
    select metric_name,date,source,qty,quality,grain,true as counted from cell_points
), original_cells as ({{ step_wall_rows('original_points', ordered_steps) }}),
current_cells as ({{ step_wall_rows('cell_points', ordered_steps) }})
select c.hour,c.source,n.avg_val,n.min_val,n.max_val,n.sample_count
from {{ cached }} c join original_cells o using(hour,source)
left join current_cells n using(hour,source)
where (c.avg_val,c.min_val,c.max_val,c.sample_count)=(o.avg_val,o.min_val,o.max_val,o.sample_count)
  and (c.avg_val,c.min_val,c.max_val,c.sample_count) is distinct from (n.avg_val,n.min_val,n.max_val,n.sample_count)
{% endmacro %}
