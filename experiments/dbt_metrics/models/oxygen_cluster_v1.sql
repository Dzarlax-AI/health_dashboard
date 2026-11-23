{{ config(enabled=var('briefing_components_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

select c.profile, c.capture, jsonb_build_object(
    'Status',case when max(valid) is null then 'missing' else 'ok' end,
    'ValidReadings',coalesce(max(valid),0),'Below94Count',coalesce(max(below94),0),
    'Below92Count',coalesce(max(below92),0),'Min',coalesce(max(minimum),0),'Avg',coalesce(max(mean),0),
    'Sources',jsonb_agg(to_jsonb(d)-array['valid','minimum','mean','below94','below92'] order by source)
              filter (where source is not null)) as evidence
from {{ source('briefing_pilot','captured_inputs') }} c
left join lateral (
    with oxygen_samples as (
        select source, qty, quality from {{ source('briefing_pilot','oxygen_samples') }} s
        where s.profile=c.profile and s.capture=c.capture
    )
    select * from ({{ oxygen_cluster_rows() }}) rows
) d on true
group by c.profile, c.capture
