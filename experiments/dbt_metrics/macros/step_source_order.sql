{% macro step_source_order(source, value) %}
({{ value }} > 0) desc nulls last,
case when {{ source }} like '%Ultra%' or {{ source }} like '%Apple Watch%' then 1
     when {{ source }} like '%iPhone%' then 2 else 3 end,
{{ value }} desc nulls last, {{ source }} collate "C" asc nulls last
{% endmacro %}
