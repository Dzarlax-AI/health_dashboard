{{ config(enabled=var('sleep_daily_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
          post_hook='alter view {{ this }} set (security_invoker=true)') }}

{% if var('tenant_native_catalog',false) %}
{{ shadow_sleep_daily_v1(adapter.quote(target.schema) ~ '.sleep_rollout_candidates',
    "profile=(select tenant_id from " ~ adapter.quote(target.schema) ~ ".__tenant_identity where singleton) and not snapshot ? 'withdrawal' and not snapshot ? 'coverage'") }}
{% elif var('sleep_daily_runtime_sql', false) %}
{{ shadow_sleep_daily_v1('sleep_shadow.candidate', 'profile = $1 AND wake_date = $2::text::date') }}
{% else %}
{{ shadow_sleep_daily_v1(source('sleep_shadow', 'candidate'), 'true') }}
{% endif %}
