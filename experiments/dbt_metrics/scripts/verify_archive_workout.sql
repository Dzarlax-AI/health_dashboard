with regrouped as (
    select p.period_id,count(w.start_time) as count,coalesce(sum(w.duration_sec),0) as total_duration_sec,
           sum(w.distance_km) as total_distance_km,sum(w.energy_kcal) as total_energy_kcal,
           avg(w.avg_hr_bpm) as avg_hr_bpm,max(w.max_hr_bpm) as max_hr_bpm,
           sum(w.hr_z1_sec) as total_hr_z1_sec,sum(w.hr_z2_sec) as total_hr_z2_sec,
           sum(w.hr_z3_sec) as total_hr_z3_sec,sum(w.hr_z4_sec) as total_hr_z4_sec,
           sum(w.hr_z5_sec) as total_hr_z5_sec
    from workout_window_audit.periods p left join source_data.workouts w
        on w.start_time between p.from_time and p.to_time and (p.name_filter='' or p.name_filter=w.name)
    group by p.period_id
), app_queries as (
    -- WorkoutStats executes one aggregate query per requested window.
    select p.period_id, a.* from workout_window_audit.periods p cross join lateral (
        select count(*) as count,coalesce(sum(duration_sec),0) as total_duration_sec,
               sum(distance_km) as total_distance_km,sum(energy_kcal) as total_energy_kcal,
               avg(avg_hr_bpm) as avg_hr_bpm,max(max_hr_bpm) as max_hr_bpm,
               sum(hr_z1_sec) as total_hr_z1_sec,sum(hr_z2_sec) as total_hr_z2_sec,
               sum(hr_z3_sec) as total_hr_z3_sec,sum(hr_z4_sec) as total_hr_z4_sec,
               sum(hr_z5_sec) as total_hr_z5_sec
        from source_data.workouts
        where start_time between p.from_time and p.to_time and (p.name_filter='' or name=p.name_filter)
    ) a
), periods as (
    select coalesce(w.period_id,m.period_id) as period,
           (to_jsonb(m)-'profile') is distinct from to_jsonb(w) as differs,
           w.count>0 as available,
           (m.count,m.max_hr_bpm,m.total_hr_z1_sec,m.total_hr_z2_sec,m.total_hr_z3_sec,m.total_hr_z4_sec,m.total_hr_z5_sec)
           is distinct from (r.count,r.max_hr_bpm,r.total_hr_z1_sec,r.total_hr_z2_sec,r.total_hr_z3_sec,r.total_hr_z4_sec,r.total_hr_z5_sec)
             or ((m.total_distance_km is null) <> (r.total_distance_km is null))
             or ((m.total_energy_kcal is null) <> (r.total_energy_kcal is null))
             or ((m.avg_hr_bpm is null) <> (r.avg_hr_bpm is null)) as grouped_non_float_diff,
           greatest(abs(m.total_duration_sec-r.total_duration_sec),abs(m.total_energy_kcal-r.total_energy_kcal),
                    abs(m.avg_hr_bpm-r.avg_hr_bpm),abs(m.total_distance_km-r.total_distance_km)) as grouped_float_delta
    from app_queries w full join archive_formula.workout_period_v1 m using(period_id)
    full join regrouped r using(period_id)
), reference_types as (
    select name,count(*) as count,min(start_time) as first_seen,max(start_time) as last_seen
    from source_data.workouts group by name
), types as (
    select (to_jsonb(m)-array['profile','sort_order']) is distinct from to_jsonb(w) as differs
    from reference_types w full join archive_formula.workout_type_v1 m using(name)
)
select json_build_object('periods',(select count(*) from periods),
       'nonempty_periods',(select count(*) from periods where available),
       'period_differences',(select count(*) from periods where differs),
       'grouped_non_float_differences',(select count(*) from periods where grouped_non_float_diff),
       'grouped_float_differences',(select count(*) from periods where grouped_float_delta>0),
       'grouped_max_float_delta',(select coalesce(max(grouped_float_delta),0) from periods),
       'types',(select count(*) from types),'type_differences',(select count(*) from types where differs));
