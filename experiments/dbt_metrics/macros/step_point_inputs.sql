{% macro step_point_inputs(points, receipts, scope) %}
select distinct on(date,source,grain) date,source,qty,quality,grain,stored_key from (
    select p.metric_name as stored_key,p.date,coalesce(p.source,'') as source,p.qty,p.quality,p.origin,p.id,
           coalesce(p.source_snapshot_at,case when p.origin<>'apple_health_xml' then p.received_at end,'-infinity'::timestamptz) as snapshot_at,
           case when p.origin='apple_health_xml' or r.content_type in ('apple-health-import','apple-health-import; catalog=1') then 'archive_sample'
                when r.content_type='application/json; metrics-contract=2' then 'hour_sum' else 'legacy_live' end as grain
    from {{ points }} p left join {{ receipts }} r on r.id=p.health_record_id
    where p.metric_name in ('step_count','__healthkit_archive_sample:step_count') and p.units='count' and ({{ scope }})
) candidates order by date,source,grain,snapshot_at desc,(origin='live') desc,id desc
{% endmacro %}
