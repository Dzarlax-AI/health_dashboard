{{ config(enabled=var('archived_profile_fields', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select ts_bucket, date, bank, drain_delta,
       restore_delta, formula_version, flags, components,
       computed_at
from {{ source('archive_records', 'energy_snapshots') }}
