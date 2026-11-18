-- Additional synthetic scenarios. Only the synthetic database owner may run
-- these checks. Every input mutation rolls back, including upon an assertion.
\set ON_ERROR_STOP on
begin;
do $$ begin
    if current_database() <> 'sem_synthetic' then raise exception 'synthetic database required'; end if;
end $$;
insert into source_data.calendar values ('2001-01-10'), ('2001-01-11');
insert into source_data.completed_night_sleep
select d::date::text, 7.5, 'case_watch', 'one', 'v1', d::date::text,
       'complete','plausible','final','eligible'
from generate_series(date '2001-01-03',date '2001-01-08', interval '1 day') d;
insert into source_data.completed_night_sleep values
('2001-01-10',6.8,'case_watch','one','v1','current','complete','plausible','provisional','ineligible');
insert into source_data.sleep_stage_day values ('2001-01-10','other','sleep_deep');
do $$ begin
  if not exists (select from dbt_output.semantic_sleep_day_v1 where wake_date='2001-01-10' and eligible_nights=6
     and status='insufficient_history' and stage_status='no_detailed_stages' and duration_minutes=408) then
     raise exception 'six-night threshold or wrong stage source'; end if;
  if (select count(*) from dbt_output.semantic_sleep_history_v1 where wake_date='2001-01-11' and reason='no_current_night')<>30 then
     raise exception 'missing wake day did not retain its decisions'; end if;
end $$;
insert into source_data.completed_night_sleep values
('2001-01-09',7.5,'case_watch','one','v1','added','complete','plausible','final','eligible');
insert into source_data.sleep_interval values
('2001-01-10','case_watch','2001-01-09 23:00Z','2001-01-10 06:00Z','sleep_core'),
('2001-01-10','case_watch','2001-01-10 02:00Z','2001-01-10 03:00Z','sleep_deep');
do $$ begin
  if not exists (select from dbt_output.semantic_sleep_duration_comparison_v1
                 where wake_date='2001-01-10' and status='available'
                   and eligible_nights=7 and duration_minutes=408 and delta_minutes=-42) then
      raise exception 'overlap changed narrow duration comparison'; end if;
  if exists (select from dbt_output.semantic_sleep_stages_v1 where wake_date='2001-01-10') then
      raise exception 'published conflicting stage durations'; end if;
end $$;
create temporary table before_correction on commit drop as
select input_version from dbt_output.semantic_sleep_day_v1 where wake_date='2001-01-10';
do $$ begin
  if not exists (select from dbt_output.semantic_sleep_day_v1 where wake_date='2001-01-10' and eligible_nights=7
    and status='available' and reference_minutes=450 and delta_minutes=-42 and stage_status='overlapping_stage_labels') then
    raise exception 'seven-night threshold, overlapping stages or rounding'; end if;
end $$;
update source_data.completed_night_sleep set duration_hours=8.5, input_hash='corrected' where wake_date='2001-01-09';
do $$ begin
  if not exists (select from dbt_output.semantic_sleep_day_v1 f cross join before_correction b
     where f.wake_date='2001-01-10' and f.delta_minutes=-51 and f.input_version<>b.input_version) then
     raise exception 'historical correction failed to revise facts'; end if;
end $$;
update source_data.completed_night_sleep set source_epoch='other' where wake_date='2001-01-09';
do $$ begin
  if not exists (select from dbt_output.semantic_sleep_history_v1 where wake_date='2001-01-10'
      and history_date='2001-01-09' and reason='epoch_changed' and not eligible) then
      raise exception 'changed epoch admitted'; end if;
end $$;
delete from source_data.sleep_interval where wake_date='2001-01-10' and stage='sleep_deep';
insert into source_data.sleep_interval values
('2001-01-10','case_watch','2001-01-10 02:00Z','2001-01-10 03:00Z','sleep_core');
do $$ begin
  if not exists (select from dbt_output.semantic_sleep_stages_v1 where wake_date='2001-01-10'
       and stage='core' and observed_minutes=420 and disjoint_intervals=1) then
       raise exception 'same-stage overlaps were double-counted'; end if;
end $$;
rollback;
