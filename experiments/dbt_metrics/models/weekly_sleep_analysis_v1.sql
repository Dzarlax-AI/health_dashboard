{{ config(enabled=var('briefing_components_shadow', false), materialized='view', post_hook="alter view {{ this }} set (security_invoker = true)") }}

with weekly_inputs as (
    select c.profile,c.capture,s."Sleep" as sleep,s."Deep" as deep,s."REM" as rem,s."Awake" as awake
    from {{ source('briefing_pilot', 'captured_inputs') }} c
    cross join lateral jsonb_to_record(c.inputs->'raw') s("Sleep" float8[],"Deep" float8[],"REM" float8[],"Awake" float8[])
)
select analysis.*, fields.* from ({{ weekly_sleep_analysis() }}) analysis
cross join lateral jsonb_to_record(analysis.weekly_sleep) fields(
  nights int, total_avg float8, deep_avg float8, rem_avg float8, awake_avg float8, efficiency float8
)
