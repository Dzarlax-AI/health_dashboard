-- Run against a disposable shadow database as fixture owner. Changes roll back.
begin;
insert into source_data.calendar values ('2000-03-26'), ('2000-10-29'), ('2000-10-30'), ('2000-10-31');
insert into source_data.daily_hrv values ('2000-10-30',38);
insert into source_data.completed_night_sleep
select date,7,'hrv_case_sleep','epoch','v1','test','complete','plausible','final','eligible'
from source_data.calendar where date in ('2000-03-26','2000-10-29','2000-10-30');
insert into source_data.sleep_interval values
('2000-03-26','hrv_case_sleep','2000-03-25 23:00+01','2000-03-26 07:00+02','core'),
('2000-10-29','hrv_case_sleep','2000-10-28 23:00+02','2000-10-29 07:00+01','core'),
('2000-10-29','hrv_case_sleep','2000-10-29 02:15+01','2000-10-29 02:16+01','awake');
insert into source_data.workout values
('case_run','running','2000-10-29 08:00+01','2000-10-29 08:30+01'),
('case_cycle','cycling','2000-10-29 08:22+01','2000-10-29 08:35+01'),
('case_spring','running','2000-03-26 01:45+01','2000-03-26 03:15+02'),
('case_invalid','running','2000-10-29 07:00+01','2000-10-29 06:00+01');
insert into source_data.hrv_point
select d,src,qty,'ok','ms',method,'test',grain,d::timestamptz,
       case when duration is not null then d::timestamptz + duration::interval end
from (values
 ('2000-03-26 01:45:00 +0100','spring_A',20,'sdnn','interval','1 minute'),
 ('2000-03-26 01:50:00 +0100','spring_A',20,'sdnn','timestamp',null),
 ('2000-03-26 01:55:00 +0100','spring_A',20,'sdnn','timestamp',null),
 ('2000-03-26 03:15:00 +0200','spring_A',80,'sdnn','timestamp',null),
 ('2000-03-26 01:46:00 +0100','spring_B',100,'unknown','timestamp',null),
 ('2000-10-29 02:15:00 +0200','fall_A',50,'sdnn','timestamp',null),
 ('2000-10-29 02:15:00 +0100','fall_A',100,'sdnn','timestamp',null),
 ('2000-10-29 07:00:00 +0100','fall_A',100,'sdnn','timestamp',null),
 ('2000-10-29 08:00:00 +0100','fall_A',50,'sdnn','interval','60 seconds'),
 ('2000-10-29 08:15:00 +0100','fall_A',70,'sdnn','timestamp',null),
 ('2000-10-29 08:23:00 +0100','fall_A',96,'sdnn','timestamp',null),
 ('2000-10-29 08:16:00 +0100','fall_A',65,'rmssd','timestamp',null),
 ('2000-10-29 08:17:00 +0100','fall_A',65,'unknown','timestamp',null),
 ('2000-10-29 08:18:00 +0100','clock',65,'sdnn','bucket','1 hour'),
 ('2000-10-29 08:30:00 +0100','fall_A',44,'sdnn','timestamp',null),
 ('2000-10-29 08:31:00 +0100','after_unknown',44,'unknown','timestamp',null),
 ('2000-10-29 08:34:00 +0100','fall_A',51,'sdnn','interval','3 minutes'),
 ('2000-10-29 08:35:00 +0100','fall_A',55,'sdnn','timestamp',null),
 ('2000-10-29 09:01:00 +0100','fall_A',56,'sdnn','timestamp',null)
) as sample(d,src,qty,method,grain,duration);
do $$ begin
 if not exists(select from dbt_output.semantic_hrv_daily_v1 where date='2000-03-26' and live_ms=75
     and abs(legacy_ms-200.0/3)<0.00001 and raw_ms=48 and n_points=5 and sdnn_points=4 and fixed_window_points=5
     and morning_origin='raw' and morning_confidence='final') then raise exception 'live, legacy, fresh or method gating'; end if;
 if not exists(select from dbt_output.semantic_hrv_daily_v1 where date='2000-10-30' and morning_ms=38
     and n_points=0 and morning_observed_on=date and morning_origin='same_day_cache' and morning_confidence='provisional')
     or exists(select from dbt_output.semantic_hrv_daily_v1 where date='2000-10-31' and morning_ms is not null)
     then raise exception 'same-day fallback'; end if;
 if not exists(select from dbt_output.semantic_hrv_night_v1 where wake_date='2000-10-29' and source='fall_A'
     and interval_points=0 and timestamp_points=1 and mean_ms=50)
     or not exists(select from dbt_output.semantic_hrv_night_v1 where wake_date='2000-03-26' and source='spring_A'
     and interval_points=1 and timestamp_points=3 and mean_ms=35)
     or not exists(select from dbt_output.semantic_hrv_night_v1 where wake_date='2000-10-30' and reason='no_sleep_interval' and mean_ms is null)
     then raise exception 'sleep interval, awake subtraction or DST'; end if;
 if not exists(select from dbt_output.semantic_hrv_workout_v1 where id='case_run' and source='fall_A'
     and interval_points=1 and timestamp_points=1 and all_timestamp_candidates=4 and unknown_method_points=1 and other_method_points=1 and ambiguous_points=1)
     or not exists(select from dbt_output.semantic_hrv_workout_v1 where id='case_cycle' and source='fall_A'
     and timestamp_points=1 and interval_points=0 and ambiguous_points=1 and boundary_points=1)
     or not exists(select from dbt_output.semantic_hrv_workout_v1 where id='case_spring' and source='spring_B'
     and interval_points=0 and timestamp_points=0 and all_timestamp_candidates=1 and unknown_method_points=1)
     or not exists(select from dbt_output.semantic_hrv_workout_v1 where id='case_run' and source='clock' and imprecise_points=1)
     or not exists(select from dbt_output.semantic_hrv_workout_v1 where id='case_invalid' and reason='invalid_workout_interval')
     then raise exception 'workout bounds, method or timestamp evidence'; end if;
 if not exists(select from dbt_output.semantic_hrv_after_workout_v1 where id='case_cycle' and source='fall_A'
     and interval_points=0 and timestamp_points=1 and timestamp_mean_ms=56 and ambiguous_points=1)
     or not exists(select from dbt_output.semantic_hrv_after_workout_v1 where id='case_spring' and source='spring_A' and timestamp_points=1 and timestamp_mean_ms=80)
     or not exists(select from dbt_output.semantic_hrv_after_workout_v1 where id='case_run' and source='after_unknown'
     and active_workout_points=1 and all_timestamp_candidates=0 and unique_window_timestamp_candidates=0)
     then raise exception 'after workout or overlapping recovery windows'; end if;
end $$;
update source_data.hrv_point set qty=40 where date='2000-03-26 01:45:00 +0100' and source='spring_A';
do $$ begin
 if not exists(select from dbt_output.semantic_hrv_daily_v1 where date='2000-03-26' and raw_ms=52)
     or not exists(select from dbt_output.semantic_hrv_workout_v1 where id='case_spring' and source='spring_A' and interval_mean_ms=40)
     then raise exception 'late correction'; end if;
end $$;
-- Same physical measurement encoded with different local offsets. Collapse
-- identical values and withhold the candidate when the values conflict.
insert into source_data.hrv_point values
 ('2000-10-29 08:05:00 +0100','case_dup',60,'ok','ms','sdnn','test','timestamp','2000-10-29 08:05+01',null),
 ('2000-10-29 07:05:00 +0000','case_dup',60,'ok','ms','sdnn','test','timestamp','2000-10-29 07:05Z',null),
 ('2000-10-29 08:06:00 +0100','case_invalid_hrv',350,'ok','ms','sdnn','test','timestamp','2000-10-29 08:06+01',null),
 ('2000-10-29 08:09:00 +0100','case_bucket',99,'ok','ms','unknown','test','bucket','2000-10-29 08:09+01',null);
do $$ begin
 if not exists(select from dbt_output.semantic_hrv_workout_v1 where id='case_run' and source='case_dup'
     and timestamp_points=1 and duplicate_points=1 and all_timestamp_candidates=1)
     or not exists(select from dbt_output.semantic_hrv_workout_v1 where id='case_run' and source='case_invalid_hrv'
     and invalid_points=1 and all_timestamp_candidates=0)
     or not exists(select from dbt_output.semantic_hrv_workout_v1 where id='case_run' and source='case_bucket'
     and imprecise_points=1 and interval_points=0 and all_timestamp_candidates=0)
     then raise exception 'duplicate, bounds of quality range or imprecise bucket label'; end if;
end $$;
update source_data.hrv_point set qty=80 where date='2000-10-29 07:05:00 +0000' and source='case_dup';
do $$ begin
 if not exists(select from dbt_output.semantic_hrv_workout_v1 where id='case_run' and source='case_dup'
     and timestamp_points=0 and duplicate_points=2 and all_timestamp_candidates=0)
     then raise exception 'conflicting repeated measurement'; end if;
end $$;
rollback;
