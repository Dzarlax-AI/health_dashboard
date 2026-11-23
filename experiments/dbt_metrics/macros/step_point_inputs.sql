{% macro step_point_inputs(points, receipts, scope, neighbor_key="to_char(b.candidate_day,'YYYY-MM-DD')") %}
with scoped as materialized (
    select p.metric_name,p.date,p.source,p.qty,p.quality,p.origin,p.id,p.health_record_id,p.source_snapshot_at,p.received_at
    from {{ points }} p where p.metric_name in ('step_count','__healthkit_archive_sample:step_count')
      and p.units='count' and ({{ scope }})
), bounds as (
    -- Opposite legal offsets can put a recording up to two calendar dates away.
    select distinct local_day + shift.n as candidate_day from (
        select distinct case when left(date,10) ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$'
                             and pg_input_is_valid(left(date,10),'date') then left(date,10)::date end as local_day
        from scoped
    ) days cross join generate_series(-2,2) shift(n) where local_day is not null
), candidates as (
    select *,true as in_scope from scoped
    union all
    select p.metric_name,p.date,p.source,p.qty,p.quality,p.origin,p.id,p.health_record_id,p.source_snapshot_at,p.received_at,false
    from bounds b join {{ points }} p on substring(p.date,1,10)={{ neighbor_key }}
    where p.metric_name in ('step_count','__healthkit_archive_sample:step_count')
      and p.units='count' and not ({{ scope }})
), exact_rows as (
    select distinct on(date,source,grain) date,source,qty,quality,grain,stored_key,in_scope from (
        select p.metric_name as stored_key,p.date,coalesce(p.source,'') as source,p.qty,p.quality,p.origin,p.id,p.in_scope,
               coalesce(p.source_snapshot_at,case when p.origin<>'apple_health_xml' then p.received_at end,'-infinity'::timestamptz) as snapshot_at,
               case when p.origin='apple_health_xml' or r.content_type in ('apple-health-import','apple-health-import; catalog=1') then 'archive_sample'
                    when r.content_type='application/json; metrics-contract=2' then 'hour_sum' else 'legacy_live' end as grain
        from candidates p left join {{ receipts }} r on r.id=p.health_record_id
    ) typed order by date,source,grain,snapshot_at desc,(origin='live') desc,id desc
), instants as (
    select *,case when quality='ok' and (qty>0 or (qty=0 and grain='hour_sum')) then {{ step_instant('e.date') }} end as instant
    from exact_rows e
), ranked as (
    select *,case when instant is null then date else first_value(date) over (
        partition by source,grain,qty,instant
        order by substring(date,22,4)='0000',date collate "C"
    ) end as canonical_date from instants
)
select date,source,qty,quality,grain,stored_key,canonical_date=date as counted,canonical_date from ranked where in_scope
{% endmacro %}
