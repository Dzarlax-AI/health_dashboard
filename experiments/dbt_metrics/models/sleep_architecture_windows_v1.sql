{{ config(enabled=var('briefing_components_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

select c.profile,c.capture,w.*
from {{ source('briefing_pilot','architecture_captures') }} c cross join lateral (
    with anchors as (select c.from_day+n as anchor from generate_series(0,c.to_day-c.from_day) n),
    architecture as (
        select * from {{ ref('sleep_architecture_days_v1') }} d where (d.profile,d.capture)=(c.profile,c.capture)
    )
    select * from ({{ sleep_architecture_windows() }}) result
) w
