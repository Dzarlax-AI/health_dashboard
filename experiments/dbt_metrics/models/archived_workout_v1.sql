{{ config(enabled=var('archived_profile_fields', false), materialized='view',
    post_hook='alter view {{ this }} set (security_invoker=true)') }}

select id, external_id, name, origin,
       source_snapshot_at, start_time, end_time, duration_sec,
       is_indoor, avg_hr_bpm, max_hr_bpm, energy_kcal,
       intensity, distance_km, avg_speed_kmh, max_speed_kmh,
       elevation_up_m, step_count_total, step_cadence_spm, temperature_c,
       humidity_pct, hr_z1_sec, hr_z2_sec, hr_z3_sec,
       hr_z4_sec, hr_z5_sec, received_at
from {{ source('archive_records', 'workouts') }}
