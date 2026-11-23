with source_points as (
    select p.*,h.id as receipt_id,h.content_type,h.payload
    from source_data.metric_points p left join source_data.health_records h on h.id=p.health_record_id
    where p.metric_name='heart_rate_variability'
), counted as (
    select count(*) as points,count(receipt_id) as linked,
        count(*) filter(where origin='apple_health_xml') as xml,
        count(*) filter(where origin='apple_health_xml' and content_type='apple-health-import; catalog=1'
            and import_run_id is not null and payload='import_run_id='||import_run_id::text) as marked_xml
    from source_points
), model_counts as (
    select count(*) as rows,count(*) filter(where method='sdnn') as sdnn,
        count(*) filter(where method='rmssd') as rmssd,count(*) filter(where method='unknown') as unknown_method,
        count(*) filter(where channel='catalog_xml_sdnn') as model_marked_xml,
        count(*) filter(where channel='explicit_live_receipt') as typed_live,
        count(*) filter(where method<>'unknown' and time_kind='interval') as typed_intervals
    from archive_formula.semantic_hrv_observation_v1
)
select row_to_json(report) from (select * from counted,model_counts) report;

-- A zero-marker archive has an independent expected method for every retained
-- point. Abort this missingness proof when measured-value verification is needed.
do $$ begin
    if exists(select from source_data.health_records where
        (content_type='apple-health-import; catalog=1' and payload ~ '^import_run_id=[0-9]+$') or
        (payload is not null and payload ~ '"(method|identifier)"[[:space:]]*:')) then
        raise exception 'archive retains method evidence; run a measured-value comparison';
    end if;
end $$;
with expected as (
    select date,coalesce(source,'') as source,qty,'unknown'::text as method from source_data.metric_points
    where metric_name='heart_rate_variability'
), observed as (
    select date,source,qty,method from archive_formula.semantic_hrv_observation_v1
), differences as (
    (select * from expected except all select * from observed) union all
    (select * from observed except all select * from expected)
), methods(metric,method) as (
    values ('hrv_observation_sdnn_v1','sdnn'),('hrv_observation_rmssd_v1','rmssd'),('hrv_observation_unknown_v1','unknown')
)
select json_build_array(m.metric,(select count(*) from expected e where e.method=m.method),
    (select count(*) from observed o where o.method=m.method),(select count(*) from differences d where d.method=m.method))
from methods m order by m.metric;
