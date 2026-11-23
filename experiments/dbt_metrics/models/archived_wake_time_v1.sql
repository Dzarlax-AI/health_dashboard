{{ config(enabled=var('archived_profile_fields', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select metric_date, state, inputs_hash, value_timestamp, calculated_at, formula_version
from {{ source('archive_records', 'derived_metrics') }}
where metric_name = 'wake_time' and value_type = 'timestamp'
