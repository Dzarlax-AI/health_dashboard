{{ config(enabled=var('briefing_components_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

select c.profile,c.capture,d.*
from {{ source('briefing_pilot','architecture_captures') }} c cross join lateral (
    with segments as (
        select s.*,c.preferred_source as preferred from {{ source('briefing_pilot','architecture_segments') }} s
        where (s.profile,s.capture)=(c.profile,c.capture)
    )
    select * from ({{ sleep_architecture_days() }}) result
) d
