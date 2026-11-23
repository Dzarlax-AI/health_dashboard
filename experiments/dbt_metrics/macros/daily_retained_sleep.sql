{% macro daily_retained_sleep(fresh, prior) %}
case when {{ fresh }}.sleep_total is not null then {{ fresh }}.sleep_total else {{ prior }}.sleep_total end::real as sleep_total,
case when {{ fresh }}.sleep_total is not null then {{ fresh }}.sleep_deep else {{ prior }}.sleep_deep end::real as sleep_deep,
case when {{ fresh }}.sleep_total is not null then {{ fresh }}.sleep_rem else {{ prior }}.sleep_rem end::real as sleep_rem,
case when {{ fresh }}.sleep_total is not null then {{ fresh }}.sleep_core else {{ prior }}.sleep_core end::real as sleep_core,
case when {{ fresh }}.sleep_total is not null then {{ fresh }}.sleep_awake else {{ prior }}.sleep_awake end::real as sleep_awake,
case when {{ fresh }}.sleep_total is not null then {{ fresh }}.sleep_unspecified else {{ prior }}.sleep_unspecified end::real as sleep_unspecified
{% endmacro %}
