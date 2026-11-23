create schema workout_capture;
create table workout_capture.sessions(
    profile text not null, external_id text not null, name text not null,
    start_time timestamptz not null,duration_sec float8 not null,distance_km float8,
    energy_kcal float8,avg_hr_bpm float8,max_hr_bpm float8,
    hr_z1_sec int,hr_z2_sec int,hr_z3_sec int,hr_z4_sec int,hr_z5_sec int,
    primary key(profile,external_id)
);
create table workout_capture.periods(
    profile text not null,period_id text not null,from_time timestamptz not null,
    to_time timestamptz not null,name_filter text not null,primary key(profile,period_id)
);
grant usage on schema workout_capture to pilot_dbt;
grant select on all tables in schema workout_capture to pilot_dbt;
