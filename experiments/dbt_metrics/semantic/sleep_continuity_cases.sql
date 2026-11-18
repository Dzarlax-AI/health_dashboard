-- Synthetic-only, fully rolled back. Dates exercise the Belgrade autumn fold.
\set ON_ERROR_STOP on
begin;
do $$ begin
    if current_database() <> 'sem_synthetic' then raise exception 'synthetic database required'; end if;
end $$;
insert into source_data.calendar values ('2000-10-29'),('2000-10-30'),('2000-10-31'),('2000-11-01');
insert into source_data.completed_night_sleep(wake_date,duration_hours,source,source_epoch,algorithm_version,
 input_hash,capture_completeness,duration_assessment,finalization_state,claim_eligibility)
select date,4,'watch-cont','e','v','h','complete','plausible','provisional','ineligible'
from source_data.calendar where date between '2000-10-29' and '2000-10-31';
insert into source_data.sleep_interval values
('2000-10-29','watch-cont','2000-10-29 00:00+02','2000-10-29 02:00+02','core'),
('2000-10-29','watch-cont','2000-10-29 00:30+02','2000-10-29 01:30+02','core'),
('2000-10-29','watch-cont','2000-10-29 02:00+02','2000-10-29 02:30+02','awake'),
('2000-10-29','watch-cont','2000-10-29 02:30+02','2000-10-29 02:00+01','awake'),
('2000-10-29','watch-cont','2000-10-29 02:00+01','2000-10-29 04:00+01','rem'),
('2000-10-29','other','2000-10-29 01:00+02','2000-10-29 02:00+02','deep'),
('2000-10-30','watch-cont','2000-10-29 23:57Z','2000-10-30 03:57Z','sleep_core');
insert into source_data.sleep_stage_day values
('2000-10-31','watch-cont','sleep_core',180),('2000-10-31','watch-cont','sleep_deep',60),
('2000-10-31','watch-cont','sleep_awake',0),('2000-10-31','other','sleep_rem',200);
do $$ begin
  if not exists(select from dbt_output.semantic_sleep_continuity_v1 where wake_date='2000-10-29'
     and stage_basis='episode_intervals' and recorded_asleep_minutes=240 and awake_minutes=60 and gap_minutes=0
     and awakening_count=1 and observed_internal_awake_bouts=1 and deep_minutes is null
     and core_fraction=0.5 and rem_fraction=0.5 and episode_end-episode_start=interval '5 hour') then
     raise exception 'DST, merging, stage shares or internal awakening count'; end if;
  if not exists(select from dbt_output.semantic_sleep_continuity_v1 where wake_date='2000-10-30'
     and awakening_count=0 and awake_minutes is null) then raise exception 'count zero and missing minutes'; end if;
  if not exists(select from dbt_output.semantic_sleep_continuity_v1 where wake_date='2000-10-31'
     and stage_basis='same_source_day_summary' and deep_fraction=0.25 and rem_fraction is null and awake_minutes=0
     and awakening_count is null and episode_start is null and continuity_reason='daily_sum_has_no_timing') then
     raise exception 'daily quantities, explicit zero and no temporal attribution'; end if;
  if not exists(select from dbt_output.semantic_sleep_continuity_v1 where wake_date='2000-11-01'
     and stage_reason='no_canonical_night' and deep_minutes is null and awakening_count is null) then
     raise exception 'calendar denominator'; end if;
end $$;
insert into source_data.sleep_interval values
('2000-10-30','watch-cont','2000-10-29 23:47Z','2000-10-29 23:57Z','sleep_awake');
update source_data.sleep_stage_day set observed_minutes=200 where date='2000-10-31' and metric_name='sleep_core';
do $$ begin
  if not exists(select from dbt_output.semantic_sleep_continuity_v1 where wake_date='2000-10-30'
     and continuity_reason='open_awake_boundary' and awake_minutes=10 and awakening_count is null
     and observed_internal_awake_bouts=0) then raise exception 'open initial awake bout'; end if;
  if not exists(select from dbt_output.semantic_sleep_continuity_v1 where wake_date='2000-10-31'
     and stage_reason='day_duration_mismatch' and deep_minutes is null) then raise exception 'inconsistent day sums'; end if;
end $$;
update source_data.sleep_interval set start_at=start_at+interval '2 minutes',end_at=end_at+interval '2 minutes'
where wake_date='2000-10-29' and source='watch-cont' and stage='rem';
update source_data.sleep_stage_day set observed_minutes=null where date='2000-10-31' and metric_name='sleep_core';
do $$ begin
  if not exists(select from dbt_output.semantic_sleep_continuity_v1 where wake_date='2000-10-29'
    and continuity_reason='gaps_in_episode' and gap_minutes=2 and awake_minutes=60 and recorded_asleep_minutes=240
    and awakening_count is null and observed_internal_awake_bouts=0) then raise exception 'gap at bout boundary'; end if;
  if not exists(select from dbt_output.semantic_sleep_continuity_v1 where wake_date='2000-10-31'
    and stage_reason='unknown_summary_quantity' and core_minutes is null) then raise exception 'unknown legacy quantity'; end if;
end $$;
insert into source_data.sleep_interval values
('2000-10-30','watch-cont','2000-10-30 01:30Z','2000-10-30 02:00Z','sleep_rem');
update source_data.completed_night_sleep set capture_completeness='partial' where wake_date='2000-10-29';
do $$ begin
  if not exists(select from dbt_output.semantic_sleep_continuity_v1 where wake_date='2000-10-30'
    and stage_reason='overlapping_stage_labels' and awake_minutes is null and awakening_count is null) then
    raise exception 'conflicting labels'; end if;
  if not exists(select from dbt_output.semantic_sleep_continuity_v1 where wake_date='2000-10-29'
    and stage_reason='capture_incomplete' and rem_minutes is null) then raise exception 'unconfirmed night'; end if;
end $$;
rollback;
