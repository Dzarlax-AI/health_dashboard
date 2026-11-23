{{ config(enabled=var('archived_profile_fields', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select effective_date, goal_hours, version, updated_at
from {{ source('archive_records', 'sleep_goal') }}
