-- Second synthetic round: keep the original overlap fixture intact, then seed
-- three isolated source epochs at the exact median/leave-one-out thresholds.
SET search_path=synthetic,public;
UPDATE research_parameters SET start_date='2026-12-01',end_date='2026-12-31',tz='Europe/Belgrade';

CREATE FUNCTION add_onset_stat_case(
 p_day date,p_epoch text,p_label text,p_interval_hours double precision
) RETURNS void LANGUAGE plpgsql AS $$
DECLARE
  source_name text := 'study_watch';
  start_at timestamptz := ((p_day - 1)::timestamp + time '22:00') AT TIME ZONE 'Europe/Belgrade';
  end_at timestamptz := start_at + interval '8 hours';
  workout_end timestamptz := start_at - p_interval_hours * interval '1 hour';
  candidate_id text := 'stats-' || p_epoch || '-' || p_day::text;
  generation text := 'generation-' || p_day::text;
  stages jsonb;
BEGIN
  stages := jsonb_build_array(jsonb_build_object(
    'date',to_char(start_at AT TIME ZONE 'Europe/Belgrade','YYYY-MM-DD"T"HH24:MI:SS') || '+01:00',
    'qty',8,'source',source_name
  ));
  PERFORM add_packet_case(p_day,8,stages,'[]'::jsonb,true,true,source_name,NULL,NULL,p_epoch);
  INSERT INTO sleep_rollout_candidates(id,revision,wake_date,snapshot)
  VALUES(candidate_id,1,p_day,jsonb_build_object(
    'revision',1,'wake_zone','Europe/Belgrade',
    'stages',jsonb_build_array(jsonb_build_object('kind','core','start_at',start_at,'end_at',end_at)),
    'legacy_aliases',jsonb_build_array(jsonb_build_object(
      'source',source_name,'source_epoch',p_epoch,'sync_generation',generation
    )),
    'coverage',jsonb_build_object('revision',1,'start_at',start_at-interval '1 hour','end_at',end_at+interval '1 hour')
  ));
  INSERT INTO sleep_rollout_daily(wake_date,candidate,revision,sleep_seconds,stage_conflict)
  VALUES(p_day,candidate_id,1,28800,false);
  INSERT INTO workouts(start_time,end_time)
  VALUES(workout_end-interval '1 hour',workout_end);
  INSERT INTO subjective_checkins(date,status,answer,prompted_at,answered_at,expires_at)
  VALUES(p_day::text,'answered',p_label,
    (p_day::timestamp+time '07:15') AT TIME ZONE 'Europe/Belgrade',
    (p_day::timestamp+time '08:00') AT TIME ZONE 'Europe/Belgrade',
    (p_day::timestamp+time '09:00') AT TIME ZONE 'Europe/Belgrade');
END $$;

-- Epoch 1: exactly five positive and five meh labels. Medians are available;
-- leave-one-out is suppressed because removing one meh would leave only four.
SELECT add_onset_stat_case(d,'epoch-five-meh',label,hours)
FROM (VALUES
 ('2026-12-01'::date,'great',2.0),('2026-12-02','great',3.0),('2026-12-03','ok',4.0),('2026-12-04','great',5.0),('2026-12-05','ok',6.0),
 ('2026-12-06','meh',1.0),('2026-12-07','meh',1.5),('2026-12-08','meh',2.0),('2026-12-09','meh',2.5),('2026-12-10','meh',3.0)
) v(d,label,hours);
-- Epoch 2: five positive and six meh labels. Six LOO omissions retain five
-- meh values, yielding finite mean and median-difference sensitivity ranges.
SELECT add_onset_stat_case(d,'epoch-six-meh',label,hours)
FROM (VALUES
 ('2026-12-11'::date,'great',3.0),('2026-12-12','great',4.0),('2026-12-13','ok',5.0),('2026-12-14','great',6.0),('2026-12-15','ok',7.0),
 ('2026-12-16','meh',1.0),('2026-12-17','meh',1.5),('2026-12-18','meh',2.0),('2026-12-19','meh',2.5),('2026-12-20','meh',3.0),('2026-12-21','meh',3.5)
) v(d,label,hours);
-- Epoch 3: four positive and six meh labels. Both median and LOO statistics
-- remain withheld because the positive sample threshold is not met.
SELECT add_onset_stat_case(d,'epoch-four-positive',label,hours)
FROM (VALUES
 ('2026-12-22'::date,'great',3.0),('2026-12-23','great',4.0),('2026-12-24','ok',5.0),('2026-12-25','great',6.0),
 ('2026-12-26','meh',1.0),('2026-12-27','meh',1.5),('2026-12-28','meh',2.0),('2026-12-29','meh',2.5),('2026-12-30','meh',3.0),('2026-12-31','meh',3.5)
) v(d,label,hours);
