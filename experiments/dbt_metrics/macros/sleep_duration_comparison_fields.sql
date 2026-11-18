{% macro sleep_duration_status() -%}
case when source is null then 'no_night'
     when not (duration_hours > 0 and duration_hours <= 24 and capture_completeness = 'complete'
               and duration_assessment = 'plausible' and (finalization_state = 'provisional'
               or (finalization_state = 'final' and claim_eligibility = 'eligible'))) then 'ineligible_night'
     when reference_hours > 0 and reference_hours <= 24 then 'available'
     else 'insufficient_history' end
{%- endmacro %}

{% macro sleep_duration_minutes(hours) -%}
(floor({{ hours }} * 60)::integer + (({{ hours }} * 60 - floor({{ hours }} * 60)) >= 0.5)::integer)
{%- endmacro %}
