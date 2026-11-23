{{ config(enabled=var('preferred_sleep_source_shadow', false) or var('briefing_components_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}
{% set input_source = 'preferred_sleep_source' if var('preferred_sleep_source_shadow', false) else 'briefing_pilot' %}

with captures as (select * from {{ source(input_source,'sri_captures') }}), segments as (
    select c.profile, c.capture, c.preferred_source, greatest(7, c.window_days) as days, s.*
    from captures c
    cross join lateral (
        select observed_at, source, metric_name, qty
        from {{ source(input_source,'sri_segments') }} s
        where (s.profile,s.capture) = (c.profile,c.capture)
          and quality = 'ok' and qty > 0
          and observed_at::date >= c.anchor - greatest(7, c.window_days) - 1
          and date_trunc('second',observed_at)::time <> time '00:00'
          and metric_name in ('sleep_total','sleep_deep','sleep_core','sleep_unspecified','sleep_rem')
    ) s
)
select * from ({{ sleep_regularity_result() }}) result
