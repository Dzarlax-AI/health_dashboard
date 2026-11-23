{{ config(enabled=var('briefing_components_shadow', false) and var('profile_metric_reads', false)) }}

-- This test targets the synthetic captures containing both physical 02:00 hours.
select profile, capture
from {{ ref('stress_hour_z_v1') }}
where hour_start >= '2000-10-29 00:00Z' and hour_start < '2000-10-29 04:00Z'
group by profile, capture
having array_agg(hour_z order by hour_start) is distinct from array[2,-1,null,0]::float8[]
