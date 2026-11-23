{{ config(enabled=var('hrv_receipt_inputs', false), materialized='view',
          post_hook='alter view {{ this }} set (security_invoker=true)') }}

-- Match only the receipt referenced by the current winning metric point.
-- Receipts may contain several sources, duplicate samples or historical corrections.
with selected as (
    select p.date,p.source,p.qty,p.quality,p.units,p.origin,p.health_record_id,p.import_run_id,
           case when p.date ~ '(Z|[+-][0-9]{2}:?[0-9]{2})$'
                    and pg_input_is_valid(p.date,'timestamptz') then p.date::timestamptz end as recorded_at
    from {{ source('source_data','metric_points') }} p
    where p.metric_name='heart_rate_variability'
), receipts as (
    select h.id,h.automation_aggregation,h.content_type,
           case when h.content_type='apple-health-import; catalog=1' then h.payload end as xml_run,
           case when pg_input_is_valid(h.payload,'jsonb') then h.payload::jsonb end as body
    from {{ source('source_data','health_records') }} h
    where h.id in (select health_record_id from selected)
), members as (
    select r.id,m->>'units' as units,x->>'date' as date,coalesce(x->>'source','') as source,x
    from receipts r cross join lateral jsonb_array_elements(
        case when jsonb_typeof(r.body#>'{data,metrics}')='array' then r.body#>'{data,metrics}' end) metrics(m)
    cross join lateral jsonb_array_elements(case when jsonb_typeof(m->'data')='array' then m->'data' end) samples(x)
    where m->>'name'='heart_rate_variability'
), evidence as (
    select p.*,r.automation_aggregation,
           (p.origin='apple_health_xml' and p.import_run_id is not null and
            r.content_type='apple-health-import; catalog=1' and r.xml_run='import_run_id='||p.import_run_id::text) as catalog_xml,
           case when count(m.x) over identity=1 and m.units=p.units
                and jsonb_typeof(m.x->'qty')='number'
                and case when pg_input_is_valid(m.x->>'qty','real') then (m.x->>'qty')::real end=p.qty
                then m.x end as sample,
           row_number() over identity as ordinal
    from selected p left join receipts r on p.health_record_id=r.id
    left join members m on p.origin='live' and (m.id,m.date,m.source)=(r.id,p.date,p.source)
    window identity as (partition by p.date,p.source)
), decoded as (
    select e.*,case when sample ? 'method' and lower(sample->>'method') in ('sdnn','rmssd')
                         then lower(sample->>'method') end as named_method,
           case when sample->>'identifier'='HKQuantityTypeIdentifierHeartRateVariabilitySDNN'
                     then 'sdnn' end as hk_method,
           case when sample->>'end_at' ~ '(Z|[+-][0-9]{2}:?[0-9]{2})$'
                     and pg_input_is_valid(sample->>'end_at','timestamptz')
                then (sample->>'end_at')::timestamptz end as recorded_end,
           case when sample->>'start_at' ~ '(Z|[+-][0-9]{2}:?[0-9]{2})$'
                     and pg_input_is_valid(sample->>'start_at','timestamptz')
                then (sample->>'start_at')::timestamptz end as explicit_start
    from evidence e where ordinal=1
), classified as (
    select *, case when catalog_xml then 'sdnn'
                   when sample ? 'method' and named_method is null then 'unknown'
                   when sample ? 'identifier' and hk_method is null then 'unknown'
                   when hk_method is not null and named_method is not null and hk_method<>named_method then 'unknown'
                   else coalesce(hk_method,named_method,'unknown') end as method,
        case when lower(coalesce(automation_aggregation,'')) similar to '%(hour|day|minute)%' then 'bucket'
             when recorded_at is null or
                (sample ? 'start_at' and explicit_start is distinct from recorded_at) then 'unknown'
             when sample ? 'end_at' then case when recorded_end>recorded_at then 'interval' else 'unknown' end
             else 'timestamp' end as time_kind
    from decoded
)
select date,coalesce(source,'') as source,qty,quality,coalesce(units,'') as unit,method,
       case when catalog_xml then 'catalog_xml_sdnn' when method<>'unknown' then 'explicit_live_receipt' when origin='apple_health_xml' then 'apple_health_xml'
            else 'live_unattributed' end as channel,time_kind,recorded_at as start_at,
       case when time_kind='interval' then recorded_end end as end_at
from classified
