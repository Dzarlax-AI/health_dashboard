{{ config(enabled=var('tenant_native_catalog',false) or var('preferred_sleep_source_shadow', false) or var('complex_sleep_parity', false),materialized='ephemeral' if var('tenant_native_catalog',false) else 'view',post_hook="alter view {{ this }} set (security_invoker = true)") }}
{% set input_source = 'preferred_sleep_source' if var('preferred_sleep_source_shadow', false) else 'complex_sleep' %}
{% if var('tenant_native_catalog',false) %}
{{ wake_candidate_result_v1(
  "select d.decision_key as profile,d.input_hash as capture,d.decision_key::date as target_day,d.inputs->>'zone' as zone,d.inputs->>'preferred_source' as preferred_source from " ~ source('source_data','hdbt_decision_inputs') ~ " d join " ~ source('source_data','hdbt_decision_inputs') ~ " s on s.metric='wake_segments' and s.formula_version=2 and s.decision_key=d.decision_key and s.input_hash=d.inputs->>'segment_hash' where d.metric='wake_confirmation' and d.formula_version=2",
  "select c.profile,c.capture,row->>'Source' as source,row->>'Metric' as metric,(row->>'StartSecond')::timestamptz as start_at,(row->>'Hours')::float8 as hours,(row->>'ReceivedAt')::timestamptz as received_at,row->>'Quality' as quality,(row->>'StartSecond')::timestamptz+(((row->>'StartNanos')::bigint+trunc((row->>'Hours')::float8*3600000000000::float8)::bigint)/1000)*interval '1 microsecond' as ends_at from " ~ source('source_data','hdbt_decision_inputs') ~ " d join " ~ source('source_data','hdbt_decision_inputs') ~ " s on s.metric='wake_segments' and s.formula_version=2 and s.decision_key=d.decision_key and s.input_hash=d.inputs->>'segment_hash' cross join lateral jsonb_array_elements(s.inputs->'segments') as a(row) cross join lateral (select d.decision_key as profile,d.input_hash as capture) c where d.metric='wake_confirmation' and d.formula_version=2"
) }}
{% else %}
{{ wake_candidate_result_v1(
  "select profile,capture,target_day,zone,preferred_source from " ~ source(input_source,'wake_segment_case'),
  "select c.profile,c.capture,s.source,s.metric,s.start_at,s.hours,s.received_at,s.quality,s.start_at+s.hours*interval '1 hour' as ends_at from " ~ source(input_source,'wake_segment_case') ~ " c join " ~ source(input_source,'wake_segment') ~ " s using(profile,capture) where s.writer_day between c.target_day-1 and c.target_day"
) }}
{% endif %}
