{{ config(enabled=var('archived_profile_fields', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select date, ts_bucket, bank, computed_at, formula_version
from {{ source('archive_records', 'energy_snapshots') }}
where 'backfilled' = any(flags)
