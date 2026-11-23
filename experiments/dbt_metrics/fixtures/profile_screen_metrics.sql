-- The Metrics catalog and chart APIs resolve units and provenance from the
-- source observations even when the active reader uses graph aggregates.
alter table health_records add column payload text,
    add column processed_at timestamptz, add column processing_status text;
insert into health_records(id,content_type,payload) values
    (1,'application/json; metrics-contract=2','{}'),
    (2,'application/json','{}');
-- Non-step legacy graph observations do not carry a count unit.
update metric_points set units=null where metric_name <> 'step_count';
-- January 1 has sub-minute legacy samples. The other fixture days carry hourly totals.
update metric_points set units='count',
    health_record_id=case when substring(date,1,10)='2026-01-01' then 2 else 1 end where metric_name='step_count';
