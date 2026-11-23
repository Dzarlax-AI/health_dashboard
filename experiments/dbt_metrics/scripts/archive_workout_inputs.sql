-- Local fixture adapter for voluntary retrospective windows. Month boundaries
-- are explicit UTC instants and each observed type also gets an all-time window.
create schema workout_window_audit;
create view workout_window_audit.sessions as
select 'archive'::text as profile,w.* from source_data.workouts w;
create view workout_window_audit.periods as
with months as (
    select distinct date_trunc('month',start_time at time zone 'UTC') at time zone 'UTC' as from_time
    from source_data.workouts
), windows as (
    select 'all' as period_id,'-infinity'::timestamptz as from_time,
           'infinity'::timestamptz as to_time,'' as name_filter
    union all
    select 'type:'||name,'-infinity'::timestamptz,'infinity'::timestamptz,name
    from (select distinct name from source_data.workouts) types
    union all
    select 'month:'||to_char(from_time at time zone 'UTC','YYYY-MM'),from_time,
           (((from_time at time zone 'UTC')+interval '1 month') at time zone 'UTC')-interval '1 microsecond',''
    from months
)
select 'archive'::text as profile,* from windows;
