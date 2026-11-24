{% macro today_metric_captures_v1(family=none) %}
{% set native = var('tenant_native_catalog', false) %}
{% if not native %}
select profile,{% if var('decision_journal_replay',false) %}to_jsonb(capture){% else %}capture{% endif %} as capture,inputs
from {{ source('briefing_pilot','captured_inputs') }}
{% endif %}
{% if native or var('decision_journal_replay',false) %}
{% if not native %}union all{% endif %}
select current_user::text as profile,
    to_jsonb({% if family == 'stress' %}'stress:'||decision_key||':'{% else %}'briefing:'{% endif %}||input_hash) as capture,
    {% if family in ('weekly','trend') %}jsonb_build_object('raw',inputs->'{{ family }}_inputs_v1'){% elif family == 'stress' %}inputs || '{"stress_capture_v1":true}'::jsonb{% else %}inputs{% endif %} as inputs
from {{ source('source_data','hdbt_decision_inputs') }}
{% if family == 'stress' %}
where metric='stress_signals' and formula_version=1
  and inputs ?& array['hour_z','z_threshold','temp','resp','drop','rhr','day_hr']
{% else %}
where metric='today_briefing' and formula_version=3
{% if family in ('weekly','trend') %}and jsonb_typeof(inputs->'{{ family }}_inputs_v1')='object'{% endif %}
{% endif %}
{% endif %}
{% endmacro %}
