{% macro workout_type_totals(sessions, scope) %}
select s.name, count(*) as count, min(s.start_time) as first_seen, max(s.start_time) as last_seen
from {{ sessions }} s
where {{ scope }}
group by s.name
order by count desc, s.name
{% endmacro %}
