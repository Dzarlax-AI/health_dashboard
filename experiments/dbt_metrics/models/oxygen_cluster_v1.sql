{{ config(enabled=var('briefing_components_shadow', false) or var('archive_oxygen', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with
{% if var('archive_oxygen', false) %}
archive_samples as materialized (
    select left(date,10) as capture,source,qty,quality
    from {{ source('source_data','metric_points') }}
    where metric_name='blood_oxygen_saturation'
), captures as (select distinct 'archive'::text as profile,capture from archive_samples),
{% else %}
captures as (select profile,capture from {{ source('briefing_pilot','captured_inputs') }}),
{% endif %}
clusters as (
select c.profile, c.capture,coalesce(max(minimum)::float8,0) as minimum_value, jsonb_build_object(
    'Status',case when max(valid) is null then 'missing' else 'ok' end,
    'ValidReadings',coalesce(max(valid),0),'Below94Count',coalesce(max(below94),0),
    'Below92Count',coalesce(max(below92),0),'Min',coalesce(max(minimum),0),'Avg',coalesce(max(mean),0),
    'Sources',jsonb_agg(to_jsonb(d)-array['valid','minimum','mean','below94','below92'] order by source)
              filter (where source is not null)) as evidence
from captures c
left join lateral (
    with oxygen_samples as (
        {% if var('archive_oxygen', false) %}
        select source,qty,quality from archive_samples s where s.capture=c.capture
        {% else %}
        select source,qty,quality from {{ source('briefing_pilot','oxygen_samples') }} s
        where s.profile=c.profile and s.capture=c.capture
        {% endif %}
    )
    select * from ({{ oxygen_cluster_rows() }}) rows
) d on true
group by c.profile, c.capture
)
select clusters.*, evidence->>'Status' as status,
       (evidence->>'ValidReadings')::integer as valid_readings,
       (evidence->>'Below94Count')::integer as below94_count,
       (evidence->>'Below92Count')::integer as below92_count,
       minimum_value as min,
       (evidence->>'Avg')::double precision as avg
from clusters
