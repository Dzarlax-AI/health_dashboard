{% macro recent_recorded_sleep(inputs) %}
with eligible as (
    select i.* from {{ inputs }} i
    where latest_date=report_date and abs(latest_hours-hours)*60<=1
      and nights>=2 and average_hours>0 and average_hours<=24
), means as (
    select *,{{ weekly_sleep_round('average_hours','100.0') }} as mean_hours from eligible
)
select mean_hours,nights,({{ weekly_sleep_round('(hours-mean_hours)*60','1.0') }})::integer as difference_minutes
from means
{% endmacro %}
