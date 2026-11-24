{% macro completed_night_duration_assessment_v1(hours) %}
case when {{ hours }} is null or {{ hours }} <= 0 then 'unknown'
     when {{ hours }} between 3 and 14 then 'plausible'
     else 'outlier' end
{% endmacro %}
