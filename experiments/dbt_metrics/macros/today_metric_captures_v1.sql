{% macro today_metric_captures_v1() %}
{% if var('decision_journal_replay', false) %}
select profile,to_jsonb(capture) as capture,inputs from {{ source('briefing_pilot','captured_inputs') }}
union all
select current_user::text,to_jsonb('briefing:'||input_hash),inputs
from {{ source('source_data','hdbt_decision_inputs') }}
where metric='today_briefing' and formula_version=3
{% else %}
select profile,capture,inputs from {{ source('briefing_pilot','captured_inputs') }}
{% endif %}
{% endmacro %}
