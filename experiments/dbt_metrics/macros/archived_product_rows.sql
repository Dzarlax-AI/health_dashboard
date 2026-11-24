{% macro archived_sleep_goal_rows() %}
select effective_date, goal_hours, version, updated_at
from {{ source('archive_records', 'sleep_goal') }}
{% endmacro %}

{% macro archived_energy_snapshot_rows() %}
select ts_bucket, date, bank, drain_delta,
       restore_delta, formula_version, flags, components,
       computed_at
from {{ source('archive_records', 'energy_snapshots') }}
{% endmacro %}

{% macro archived_workout_rows(with_location=false) %}
select id, external_id, name, origin,
       source_snapshot_at, start_time, end_time, duration_sec,
       is_indoor, avg_hr_bpm, max_hr_bpm, energy_kcal,
       intensity, distance_km, avg_speed_kmh, max_speed_kmh,
       elevation_up_m, step_count_total, step_cadence_spm, temperature_c,
       humidity_pct, hr_z1_sec, hr_z2_sec, hr_z3_sec,
       hr_z4_sec, hr_z5_sec, received_at{% if with_location %}, location{% endif %}
from {{ source('archive_records', 'workouts') }}
{% endmacro %}
