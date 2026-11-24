{{ config(enabled=var('complex_sleep_parity',false) or var('tenant_native_catalog',false),materialized='ephemeral' if var('tenant_native_catalog',false) else 'view',post_hook="alter view {{ this }} set (security_invoker = true)") }}

{{ sleep_balance_window_v1(ref('sleep_balance_period_v1')) }}
