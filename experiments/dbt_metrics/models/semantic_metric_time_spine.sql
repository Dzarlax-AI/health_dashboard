{{ config(enabled=var('semantic_shadow', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}
select date::date as day from {{ source('source_data','calendar') }}
