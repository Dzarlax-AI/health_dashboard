{% macro step_wall_rows(points, ordered_steps=false) %}
with counted_points as (select * from {{ points }} where counted), positives as (
  {{ hourly_sum_rows('counted_points', ordered_steps) }}
), observed_zeros as (
  select 'step_count'::text as metric_name,substring(date,1,13)||':00' as hour,
         source,0::real as avg_val,0::real as min_val,0::real as max_val,1::int as sample_count
  from counted_points where grain='hour_sum' and quality='ok' and qty=0
  group by substring(date,1,13),source
)
select * from positives union all
select z.* from observed_zeros z where not exists(select from positives p where (p.hour,p.source)=(z.hour,z.source))
{% endmacro %}
