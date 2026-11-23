{{ config(enabled=var('archived_profile_fields', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select wake_date, source, source_epoch, algorithm_version,
       finalized_at, covered_interval_start, covered_interval_end, observed_at,
       coalesce(diagnostics->>'preferred_sleep_source','') as preferred_sleep_source,
       coalesce(diagnostics->>'source_selection_reason','') as source_selection_reason
from {{ source('archive_records', 'completed_night_sleep') }}
