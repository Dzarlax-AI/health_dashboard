{{ config(enabled=var('briefing_components_shadow', false)) }}

with input_cases(id, latest_date, report_date, hours, latest_hours, nights, average_hours) as (
    values
      (1,'2026-10-20','2026-10-20',6.85::float8,6.85::float8,7,7.59::float8),
      (2,'2026-10-20','2026-10-20',7.125,7.125,2,7),
      (3,'2026-10-20','2026-10-20',6.875,6.875,2,7),
      (4,'2026-10-20','2026-10-20',7,7,1,7),
      (5,'2026-10-19','2026-10-20',7,7,3,7),
      (6,'2026-10-20','2026-10-20',7,6,3,7)
), actual as (
    select c.id,r.* from input_cases c
    cross join lateral (
        with inputs as (select c.*)
        select * from ({{ recent_recorded_sleep('inputs') }}) calculated
    ) r
), expected(id,mean_hours,nights,difference_minutes) as (
    values (1,7.59::float8,7,-44),(2,7,2,8),(3,7,2,-8)
)
select * from (table expected except all table actual) missing
union all
select * from (table actual except all table expected) extra
