{% macro step_instant(date_expr) %}
case when {{ date_expr }} ~ '^[0-9]{4}-[0-9]{2}-[0-9]{2} ([01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9] [+-]([01][0-9]|2[0-3])[0-5][0-9]$'
          and pg_input_is_valid(left({{ date_expr }},19),'timestamp without time zone')
     then left({{ date_expr }},19)::timestamp - make_interval(mins =>
          (case substring({{ date_expr }},21,1) when '-' then -1 else 1 end) *
          (substring({{ date_expr }},22,2)::int*60 + substring({{ date_expr }},24,2)::int))
end
{% endmacro %}
