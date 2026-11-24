{{ config(enabled=var('archived_profile_fields', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select sub_score, target_kind, source_epoch,
       method, calibration_window_days, computed_at
from {{ source('archive_records', 'chip_calibrations') }}
