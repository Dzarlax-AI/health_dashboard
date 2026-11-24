{{ config(enabled=var('tenant_native_catalog',false) or var('complex_sleep_parity',false),materialized='ephemeral' if var('tenant_native_catalog',false) else 'view',post_hook="alter view {{ this }} set (security_invoker = true)") }}
{% if var('tenant_native_catalog',false) %}
with replay as (
{{ wake_confirmation_result_v1(
  "select c.profile,c.capture,c.candidate_wake,c.signal,c.source,c.latest_ingest,(d.inputs->>'decision_at')::timestamptz as decision_at,(d.inputs->>'quiet_since')::timestamptz as quiet_since,d.inputs->>'zone' as zone,(d.inputs->>'typical_minute')::int as typical_minute,(d.inputs->>'steps')::float8 as steps,d.inputs->>'sleep_inputs_hash' as inputs_hash,d.result as observed from " ~ ref('wake_candidate_v1') ~ " c join " ~ source('source_data','hdbt_decision_inputs') ~ " d on d.metric='wake_confirmation' and d.formula_version=2 and d.decision_key=c.profile and d.input_hash=c.capture"
) }}
)
select *,profile as metric_date,capture as revision
from replay
where candidate_wake=(observed->>'StoredWake')::timestamptz
  and source=observed->>'input_source' and signal=observed->>'signal'
  and confidence=observed->>'confidence' and ready=(observed->>'ready')::boolean
  and reason=observed->>'reason'
{% else %}
{{ wake_confirmation_result_v1(
  "select profile,capture,candidate_wake,signal,null::text as source,latest_ingest,decision_at,quiet_since,zone,typical_minute,steps,''::text as inputs_hash,'{}'::jsonb as observed from " ~ source('complex_sleep','wake_capture')
) }}
{% endif %}
