{{ config(enabled=var('tenant_native_catalog', false) or var('briefing_components_shadow', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select profile,capture,d.energy_bank is not null and d.energy_bank<>'null' as has_bank,
       d.sleep is not null and d.sleep<>'null' as has_sleep,
       (d.sleep->>'latest_total')::float8 as latest_total,
       case when jsonb_typeof(d.energy_bank->'flags')='array'
            then array(select jsonb_array_elements_text(d.energy_bank->'flags')) end as flags
from ({{ today_metric_captures_v1() }}) c
cross join lateral jsonb_to_record(c.inputs->'decision') d(sleep jsonb,energy_bank jsonb)
