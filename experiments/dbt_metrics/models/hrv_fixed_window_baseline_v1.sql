{{ config(enabled=var('personal_baseline_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select profile, capture, anchor, baseline_median, mad_sd, sample_count, state
from {{ ref('personal_baseline_v1') }}
where channel = 'hrv' and (window_days <= 0 or window_days = 30)
