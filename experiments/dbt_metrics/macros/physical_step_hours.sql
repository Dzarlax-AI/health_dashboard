{% macro physical_step_hours(points, cached) %}
with step_input as (select * from {{ points }}), wall_cache as (select * from {{ cached }}),
incomplete as ({{ incomplete_step_hours('step_input','wall_cache') }}),
ambiguous as ({{ ambiguous_step_hours('step_input') }}),
uncached_wall_points as (
    select 'step_count'::text as metric_name,p.* from step_input p
    join ambiguous a on a.hour=substring(p.date,1,13)||':00'
    where not exists(select from wall_cache c where c.hour=a.hour)
), uncached_wall_rows as ({{ step_wall_rows('uncached_wall_points') }}),
minutes as (
    select substring(date,1,16) as minute,substring(date,21,5) as utc_offset,source,grain,
           case when grain='legacy_live' then max(qty) else sum(qty::float8) end as quantity,
           min(qty) as lo,max(qty) as hi
    from step_input where not exists(select from ambiguous a where a.hour=substring(step_input.date,1,13)||':00')
      and quality='ok' and (qty>0 or (qty=0 and grain='hour_sum'))
    group by substring(date,1,16),substring(date,21,5),source,grain
), grains as (
    select substring(minute,1,13)||':00' as hour,utc_offset,source,grain,
           sum(quantity) as avg_val,min(lo) as min_val,max(hi) as max_val
    from minutes group by substring(minute,1,13),utc_offset,source,grain
), sources as (
    select distinct on(hour,utc_offset,source) hour,utc_offset,source,avg_val,min_val,max_val
    from grains where not exists (select from incomplete i where i.hour=grains.hour)
    order by hour,utc_offset,source,(grain='hour_sum') desc,(grain='legacy_live') desc
), candidates as (
    select * from sources union all
    select hour,''::text,source,avg_val,min_val,max_val from uncached_wall_rows union all
    select c.hour,''::text,c.source,c.avg_val,c.min_val,c.max_val
    from wall_cache c join incomplete i using(hour)
)
select distinct on(hour,utc_offset) hour,utc_offset,source,avg_val,min_val,max_val
from candidates order by hour,utc_offset,{{ step_source_order('source','avg_val') }}
{% endmacro %}
