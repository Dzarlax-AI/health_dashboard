{% macro proven_step_edits(points, cached, incoming, previous, scope, full_snapshot, ordered_steps=false) %}
with signatures as ({{ step_capture_signature(points) }}), unproven as (
    select c.* from {{ cached }} c where not exists(select from signatures t where (c.hour,c.proven_input)=(t.hour,t.signature))
), frozen as ({{ incomplete_step_hours(points,'unproven', ordered_steps) }}),
edits as (
    select distinct substring(n.date,1,13)||':00' as hour
    from {{ incoming }} n left join {{ previous }} old using(metric_name,date,source)
    where n.metric_name in ('step_count','__healthkit_archive_sample:step_count') and {{ scope }}
      and (n.qty,n.quality,n.origin,n.health_record_id,n.units,n.source_snapshot_at,n.received_at,n.id) is distinct from
          (old.qty,old.quality,old.origin,old.health_record_id,old.units,old.source_snapshot_at,old.received_at,old.id)
      and n.units='count' and n.quality='ok' and n.qty>=0
)
select e.hour from edits e where not exists(select from frozen f where f.hour=e.hour)
and (not {{ full_snapshot }} or not exists (
    select from {{ previous }} old where old.metric_name in ('step_count','__healthkit_archive_sample:step_count')
      and substring(old.date,1,10)=substring(e.hour,1,10) and substring(old.date,1,13)||':00'=e.hour
      and not exists(select from {{ incoming }} n where (n.metric_name,n.date,n.source)=(old.metric_name,old.date,old.source))
  ))
{% endmacro %}
