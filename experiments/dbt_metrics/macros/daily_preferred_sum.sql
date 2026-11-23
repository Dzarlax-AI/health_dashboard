{% macro daily_preferred_sum(watch, phone) %}
coalesce(max(sum_val) filter(where {{ watch }}), max(sum_val) filter(where {{ phone }}), max(sum_val))
{% endmacro %}
