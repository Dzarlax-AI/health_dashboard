{{ config(enabled=var('sleep_daily_shadow', false), materialized='view',
          post_hook='alter view {{ this }} set (security_invoker=true)') }}

{% if var('sleep_daily_runtime_sql', false) %}
{{ shadow_sleep_daily_v1('sleep_shadow.candidate', 'profile = $1 AND wake_date = $2::text::date') }}
{% else %}
{{ shadow_sleep_daily_v1(source('sleep_shadow', 'candidate'), 'true') }}
{% endif %}
