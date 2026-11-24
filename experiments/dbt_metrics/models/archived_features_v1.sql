{{ config(enabled=var('archived_profile_fields', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select f.date, f.sub_score, f.source_epoch, f.feature_version, f.computed_at, e.*
from {{ source('archive_records', 'feature_snapshots') }} f
cross join lateral {{ chip_feature_record('f.features') }}
