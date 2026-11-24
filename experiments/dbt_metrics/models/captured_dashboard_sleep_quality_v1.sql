{{ config(enabled=var('tenant_native_catalog', false) or var('briefing_components_shadow', false), materialized='ephemeral' if var('tenant_native_catalog', false) else 'view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

-- computeDashboardSleepQuality reads four component states in order and stops
-- at the first missing one. The factor formula is shared with the night card.
with captures as (
    select profile,capture,inputs#>'{raw,ReadinessEvidence}' as evidence
    from ({{ today_metric_captures_v1() }}) c
    where jsonb_typeof(inputs->'raw')='object'
    {% if var('tenant_native_catalog', false) %}and inputs#>'{raw,ReadinessEvidence}' ?&
        array['SleepQuality','SleepDuration','SleepDeep','SleepREM','SleepAwake']{% endif %}
), evaluated as ({{ dashboard_sleep_quality('captures') }})
select * from evaluated
