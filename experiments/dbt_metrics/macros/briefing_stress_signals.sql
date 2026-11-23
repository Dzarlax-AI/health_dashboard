{% macro briefing_stress_signals() %}
with hours as (
    select position, case when z > '-Infinity'::float8 and z < 'Infinity'::float8 then z end as z
    from unnest(i.hour_z) with ordinality as h(z, position)
), runs as (
    select z, position, count(*) filter (where z is null or z <= 1)
           over (order by position) as run_id
    from hours
)
select coalesce((select sum(z - i.threshold order by position) from hours
                 where i.threshold > '-Infinity'::float8 and i.threshold < 'Infinity'::float8
                   and z > i.threshold), 0) as load,
       exists(select from hours where z > 2) as acute,
       exists(select from runs where z > 1 group by run_id having count(*) >= 4) as sustained,
       coalesce(i.temp > 1 and i.temp < 'Infinity'::float8
            and i.resp > 1 and i.resp < 'Infinity'::float8
            and i.hrv_drop > 1 and i.hrv_drop < 'Infinity'::float8, false) as illness_signature,
       coalesce(i.hrv_drop > 1 and i.hrv_drop < 'Infinity'::float8
            and i.rhr > 0.5 and i.rhr < 'Infinity'::float8, false) as recovery_debt,
       coalesce(i.day_hr > 1 and i.day_hr < 'Infinity'::float8
            and i.hrv_drop < -1 and i.hrv_drop > '-Infinity'::float8, false) as rebound
{% endmacro %}
