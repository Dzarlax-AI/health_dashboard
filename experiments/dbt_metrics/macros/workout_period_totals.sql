{% macro workout_period_totals(sessions, scope) %}
select count(*) as count, coalesce(sum(s.duration_sec),0) as total_duration_sec,
       sum(s.distance_km) as total_distance_km, sum(s.energy_kcal) as total_energy_kcal,
       avg(s.avg_hr_bpm) as avg_hr_bpm, max(s.max_hr_bpm) as max_hr_bpm,
       sum(s.hr_z1_sec) as total_hr_z1_sec, sum(s.hr_z2_sec) as total_hr_z2_sec,
       sum(s.hr_z3_sec) as total_hr_z3_sec, sum(s.hr_z4_sec) as total_hr_z4_sec,
       sum(s.hr_z5_sec) as total_hr_z5_sec
from {{ sessions }} s
where {{ scope }}
{% endmacro %}
