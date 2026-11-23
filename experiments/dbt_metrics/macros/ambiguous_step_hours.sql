{% macro ambiguous_step_hours(points) %}
select distinct substring(date,1,13)||':00' as hour from {{ points }}
where quality='ok' and (qty>0 or (qty=0 and grain='hour_sum'))
  and substring(date,21) !~ '^[+-]([01][0-9]|2[0-3])[0-5][0-9]$'
{% endmacro %}
