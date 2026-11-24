{{ config(enabled=var('briefing_components_shadow', false) or var('tenant_native_catalog', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

with hours as (
select c.profile, c.capture, h.*
{% if var('tenant_native_catalog', false) %}
from (select current_user::text as profile,input_hash as capture,inputs from {{ adapter.quote(target.schema) }}.hdbt_decision_inputs
      where metric='stress_signals' and formula_version=1 and jsonb_typeof(inputs->'stress_hours')='array') c
{% else %}from {{ source('briefing_pilot', 'captured_inputs') }} c{% endif %}
cross join lateral jsonb_to_recordset(nullif(c.inputs->'stress_hours','null'::jsonb)) as h(
    hour_start timestamptz, coverage_ok boolean, hour_hr float8, baseline float8, sd float8)
), scored as ({{ hourly_stress('hours') }})
select profile,capture,hour_start,coverage_ok,hour_z from scored
