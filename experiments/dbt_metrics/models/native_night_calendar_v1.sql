{{ config(enabled=var('tenant_native_catalog',false), materialized='ephemeral') }}
select date from {{ adapter.quote(target.schema) }}.daily_scores
union
select wake_date from {{ source('source_data','completed_night_sleep') }}
