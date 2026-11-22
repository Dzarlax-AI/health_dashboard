-- Add the earlier snapshot inputs and outcomes to the isolated raw-packet fixture.
-- Nov 1 agrees, Nov 2 disagrees by one hour, Nov 5 is snapshot-only and Nov 14 packet-only.
SET search_path=synthetic,public;
UPDATE research_parameters SET end_date='2026-11-20';
CREATE TABLE sleep_rollout_candidates(
    id text PRIMARY KEY, revision bigint, wake_date date, snapshot jsonb
);
CREATE TABLE sleep_rollout_daily(
    wake_date date PRIMARY KEY, candidate text, revision bigint, sleep_seconds double precision, stage_conflict boolean
);
INSERT INTO sleep_rollout_candidates
SELECT d::text,1,d,jsonb_build_object('stages',jsonb_build_array(
    jsonb_build_object('kind','core','start_at',s,'end_at',s+interval '8 hours')))
FROM (VALUES
    (date '2026-11-01', timestamptz '2026-10-31 22:00+01'),
    (date '2026-11-02', timestamptz '2026-11-01 23:00+01'),
    (date '2026-11-05', timestamptz '2026-11-04 22:00+01')
) snapshots(d,s);
INSERT INTO sleep_rollout_daily SELECT wake_date,id,revision,28800,false FROM sleep_rollout_candidates;
CREATE TABLE workouts(start_time timestamptz,end_time timestamptz);
CREATE TABLE subjective_checkins(
    date text, status text, answer text, prompted_at timestamptz, answered_at timestamptz, expires_at timestamptz
);
INSERT INTO workouts
SELECT (d-1+time '18:00') AT TIME ZONE 'Europe/Belgrade',
       (d-1+time '19:00') AT TIME ZONE 'Europe/Belgrade'
FROM unnest(ARRAY[date '2026-11-01',date '2026-11-02',date '2026-11-05',date '2026-11-14']) dates(d);
INSERT INTO subjective_checkins
SELECT d::text,'answered',answer,
       (d+time '07:15') AT TIME ZONE 'Europe/Belgrade',
       (d+time '08:00') AT TIME ZONE 'Europe/Belgrade',
       (d+time '09:00') AT TIME ZONE 'Europe/Belgrade'
FROM (VALUES (date '2026-11-01','ok'),(date '2026-11-02','great'),
             (date '2026-11-05','meh'),(date '2026-11-14','sick')) feedback(d,answer);
