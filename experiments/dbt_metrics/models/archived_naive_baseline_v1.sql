{{ config(enabled=var('archived_profile_fields', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select date, sub_score, target_kind, baseline_kind, source_epoch,
       formula_version, computed_at
from {{ source('archive_records', 'naive_baselines') }}
