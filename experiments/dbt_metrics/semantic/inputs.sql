-- Isolated shadow input contract. One profile per schema and per database role.
-- calendar lists evaluation dates only. Observation and night history may precede it.
create schema source_data;
create table source_data.calendar(date text primary key);
create table source_data.completed_night_sleep(
 wake_date text primary key, duration_hours double precision not null, source text not null,
 source_epoch text not null, algorithm_version text not null, input_hash text not null,
 capture_completeness text not null, duration_assessment text not null, finalization_state text not null,
 claim_eligibility text not null, observed_at timestamptz);
-- Only canonical same-source, same-episode evidence may populate these intervals.
create table source_data.sleep_interval(
 wake_date text references source_data.completed_night_sleep, source text not null,
 start_at timestamptz not null, end_at timestamptz not null, stage text not null,
 primary key(wake_date,source,start_at,end_at,stage), check(end_at>start_at));
create table source_data.hrv_point(
 date text not null, source text not null, qty real, quality text not null,
 unit text not null, method text not null, channel text not null,
 time_kind text not null, start_at timestamptz, end_at timestamptz,
 primary key(date,source), check(method in ('sdnn','rmssd','unknown')),
 check(time_kind in ('interval','timestamp','bucket','unknown')),
 check(time_kind <> 'interval' or (start_at is not null and end_at is not null and end_at>start_at)),
 check(time_kind <> 'timestamp' or start_at is not null));
create table source_data.workout(
 id text primary key, kind text not null, start_at timestamptz not null, end_at timestamptz not null);
create table source_data.daily_hrv(date text primary key, hrv_avg real);
-- Aggregate stage observations may lack episode attribution. Keep them distinct from intervals.
create table source_data.sleep_stage_day(
 date text not null, source text not null, metric_name text not null,
 observed_minutes double precision check (observed_minutes between 0 and 1440),
 primary key(date,source,metric_name));
