CREATE SCHEMA synthetic;
SET search_path=synthetic,public;

CREATE TABLE research_parameters(start_date date,end_date date,tz text);
INSERT INTO research_parameters VALUES ('2026-11-01','2026-11-12','Europe/Belgrade');
CREATE TABLE completed_night_sleep(
 wake_date text PRIMARY KEY,duration_hours double precision NOT NULL,source text NOT NULL,
 source_epoch text NOT NULL,input_hash text NOT NULL,capture_completeness text NOT NULL,
 duration_assessment text NOT NULL,finalization_state text NOT NULL,claim_eligibility text NOT NULL,
 coverage_generation text NOT NULL,observed_at timestamptz NOT NULL
);
CREATE TABLE sleep_rollout_daily(
 wake_date date PRIMARY KEY,generation bigint NOT NULL,candidate text,revision bigint,
 sleep_seconds double precision,stage_conflict boolean
);
CREATE TABLE sleep_rollout_candidates(
 id text PRIMARY KEY,revision bigint NOT NULL,wake_date date NOT NULL,snapshot jsonb NOT NULL
);
CREATE TABLE workouts(start_time timestamptz NOT NULL,end_time timestamptz NOT NULL);
CREATE TABLE subjective_checkins(
 date text NOT NULL,source text NOT NULL,status text NOT NULL,answer text,
 prompted_at timestamptz NOT NULL,answered_at timestamptz,expires_at timestamptz NOT NULL,
 PRIMARY KEY(date,source)
);
-- Include the XML/live aggregate surface in its production shape. The row model
-- intentionally reads selected rollout snapshots and never mixes these totals
-- into direct stage timestamps.
CREATE TABLE metric_points(
 date text NOT NULL,source text NOT NULL,metric_name text NOT NULL,units text,
 qty real,quality text,origin text NOT NULL,import_run_id bigint
);

INSERT INTO completed_night_sleep
SELECT d::date::text,8,'watch','epoch-a','synthetic-'||d::date,'complete','plausible','final','eligible',
 'gen-'||d::date,'2026-11-12 10:00+01'::timestamptz
FROM generate_series('2026-11-01'::date,'2026-11-12'::date,interval '1 day') d;
UPDATE completed_night_sleep SET capture_completeness='partial' WHERE wake_date='2026-11-06';
UPDATE completed_night_sleep SET source='ring',source_epoch='epoch-a' WHERE wake_date='2026-11-11';

-- Each selected row points at an exact revision, like the production daily
-- selection. Candidate IDs retain the source component used by real packets.
INSERT INTO sleep_rollout_candidates(id,revision,wake_date,snapshot) VALUES
 ('["watch","valid"]',1,'2026-11-01',jsonb_build_object('revision',1,'wake_zone','Europe/Belgrade','stages',jsonb_build_array(
   jsonb_build_object('kind','core','start_at','2026-10-31 22:00+01','end_at','2026-11-01 00:00+01'),
   jsonb_build_object('kind','deep','start_at','2026-11-01 00:00+01','end_at','2026-11-01 02:00+01'),
   jsonb_build_object('kind','rem','start_at','2026-11-01 02:00+01','end_at','2026-11-01 03:00+01'),
   jsonb_build_object('kind','core','start_at','2026-11-01 03:00+01','end_at','2026-11-01 06:00+01')),
   'legacy_aliases',jsonb_build_array(jsonb_build_object('source','watch','source_epoch','epoch-a','sync_generation','gen-2026-11-01')),
   'coverage',jsonb_build_object('revision',1,'start_at','2026-10-31 21:00+01','end_at','2026-11-01 06:30+01'))),
 ('["watch","nap"]',2,'2026-11-02',jsonb_build_object('revision',2,'wake_zone','Europe/Belgrade','stages',jsonb_build_array(
   jsonb_build_object('kind','core','start_at','2026-11-01 22:00+01','end_at','2026-11-02 02:00+01'),
   jsonb_build_object('kind','deep','start_at','2026-11-02 02:00+01','end_at','2026-11-02 04:00+01'),
   jsonb_build_object('kind','core','start_at','2026-11-02 04:00+01','end_at','2026-11-02 06:00+01'),
   jsonb_build_object('kind','core','start_at','2026-11-02 14:00+01','end_at','2026-11-02 15:00+01')),
   'legacy_aliases',jsonb_build_array(jsonb_build_object('source','watch','source_epoch','epoch-a','sync_generation','gen-2026-11-02')),
   'coverage',jsonb_build_object('revision',2,'start_at','2026-11-01 21:00+01','end_at','2026-11-02 06:30+01'))),
 ('["watch","wake"]',3,'2026-11-03',jsonb_build_object('revision',3,'wake_zone','Europe/Belgrade','stages',jsonb_build_array(
   jsonb_build_object('kind','core','start_at','2026-11-02 22:00+01','end_at','2026-11-03 02:00+01'),
   jsonb_build_object('kind','awake','start_at','2026-11-03 02:00+01','end_at','2026-11-03 02:20+01'),
   jsonb_build_object('kind','rem','start_at','2026-11-03 02:20+01','end_at','2026-11-03 06:20+01')),
   'legacy_aliases',jsonb_build_array(jsonb_build_object('source','watch','source_epoch','epoch-a','sync_generation','gen-2026-11-03')),
   'coverage',jsonb_build_object('revision',3,'start_at','2026-11-02 21:00+01','end_at','2026-11-03 06:30+01'))),
 ('["watch","split"]',4,'2026-11-04',jsonb_build_object('revision',4,'wake_zone','Europe/Belgrade','stages',jsonb_build_array(
   jsonb_build_object('kind','core','start_at','2026-11-03 22:00+01','end_at','2026-11-04 02:00+01'),
   jsonb_build_object('kind','core','start_at','2026-11-04 04:00+01','end_at','2026-11-04 08:00+01')),
   'legacy_aliases',jsonb_build_array(jsonb_build_object('source','watch','source_epoch','epoch-a','sync_generation','gen-2026-11-04')))),
 ('["watch","switch"]',5,'2026-11-05',jsonb_build_object('revision',5,'wake_zone','Europe/Belgrade','stages',jsonb_build_array(
   jsonb_build_object('kind','core','start_at','2026-11-04 22:00+01','end_at','2026-11-05 06:00+01')),
   'legacy_aliases',jsonb_build_array(jsonb_build_object('source','watch','source_epoch','epoch-old','sync_generation','old-generation')))),
 ('["watch","partial"]',6,'2026-11-06',jsonb_build_object('revision',6,'wake_zone','Europe/Belgrade','stages',jsonb_build_array(
   jsonb_build_object('kind','core','start_at','2026-11-05 22:00+01','end_at','2026-11-06 06:00+01')),
   'legacy_aliases',jsonb_build_array(jsonb_build_object('source','watch','source_epoch','epoch-a','sync_generation','gen-2026-11-06')),
   'coverage',jsonb_build_object('revision',6,'start_at','2026-11-05 21:00+01','end_at','2026-11-06 06:30+01'))),
 ('["watch","boundary-awake"]',7,'2026-11-07',jsonb_build_object('revision',7,'wake_zone','Europe/Belgrade','stages',jsonb_build_array(
   jsonb_build_object('kind','awake','start_at','2026-11-06 21:45+01','end_at','2026-11-06 22:00+01'),
   jsonb_build_object('kind','core','start_at','2026-11-06 22:00+01','end_at','2026-11-07 06:00+01')),
   'legacy_aliases',jsonb_build_array(jsonb_build_object('source','watch','source_epoch','epoch-a','sync_generation','gen-2026-11-07')),
   'coverage',jsonb_build_object('revision',7,'start_at','2026-11-06 21:00+01','end_at','2026-11-07 06:30+01'))),
 ('["watch","overlap"]',8,'2026-11-08',jsonb_build_object('revision',8,'wake_zone','Europe/Belgrade','stages',jsonb_build_array(
   jsonb_build_object('kind','core','start_at','2026-11-07 22:00+01','end_at','2026-11-08 02:00+01'),
   jsonb_build_object('kind','deep','start_at','2026-11-08 01:30+01','end_at','2026-11-08 03:00+01'),
   jsonb_build_object('kind','core','start_at','2026-11-08 03:00+01','end_at','2026-11-08 06:00+01')),
   'legacy_aliases',jsonb_build_array(jsonb_build_object('source','watch','source_epoch','epoch-a','sync_generation','gen-2026-11-08')),
   'coverage',jsonb_build_object('revision',8,'start_at','2026-11-07 21:00+01','end_at','2026-11-08 06:30+01'))),
 ('["watch","before-checkin"]',9,'2026-11-09',jsonb_build_object('revision',9,'wake_zone','Europe/Belgrade','stages',jsonb_build_array(
   jsonb_build_object('kind','core','start_at','2026-11-08 22:00+01','end_at','2026-11-09 06:00+01')),
   'legacy_aliases',jsonb_build_array(jsonb_build_object('source','watch','source_epoch','epoch-a','sync_generation','gen-2026-11-09')),
   'coverage',jsonb_build_object('revision',9,'start_at','2026-11-08 21:00+01','end_at','2026-11-09 06:30+01'))),
 ('["ring","source-switch"]',10,'2026-11-11',jsonb_build_object('revision',10,'wake_zone','Europe/Belgrade','stages',jsonb_build_array(
   jsonb_build_object('kind','core','start_at','2026-11-10 22:00+01','end_at','2026-11-11 06:00+01')),
   'legacy_aliases',jsonb_build_array(jsonb_build_object('source','ring','source_epoch','epoch-a','sync_generation','gen-2026-11-11')))),
 ('["watch","bound"]',11,'2026-11-12',jsonb_build_object('revision',11,'wake_zone','Europe/Belgrade','stages',jsonb_build_array(
   jsonb_build_object('kind','core','start_at','2026-11-11 22:00+01','end_at','2026-11-12 06:00+01')),
   'legacy_aliases',jsonb_build_array(jsonb_build_object('source','watch','source_epoch','epoch-a','sync_generation','gen-2026-11-12')),
   'coverage',jsonb_build_object('revision',11,'start_at','2026-11-11 21:00+01','end_at','2026-11-12 06:30+01')));
UPDATE sleep_rollout_candidates SET snapshot=jsonb_set(snapshot,'{coverage}',
 snapshot->'coverage'||jsonb_build_object('source_id','watch','observed_at','2026-11-12 10:00+01'::timestamptz))
WHERE snapshot ? 'coverage';
INSERT INTO sleep_rollout_daily(wake_date,generation,candidate,revision,sleep_seconds,stage_conflict) VALUES
 ('2026-11-01',1,'["watch","valid"]',1,28800,false),
 ('2026-11-02',2,'["watch","nap"]',2,28800,false),
 ('2026-11-03',3,'["watch","wake"]',3,28800,false),
 ('2026-11-04',4,'["watch","split"]',4,28800,false),
 ('2026-11-05',5,'["watch","switch"]',5,28800,false),
 ('2026-11-06',6,'["watch","partial"]',6,28800,false),
 ('2026-11-07',7,'["watch","boundary-awake"]',7,28800,false),
 ('2026-11-08',8,'["watch","overlap"]',8,28800,true),
 ('2026-11-09',9,'["watch","before-checkin"]',9,28800,false),
 ('2026-11-11',10,'["ring","source-switch"]',10,28800,false),
 ('2026-11-12',11,'["watch","bound"]',11,28800,false);

-- A direct XML row coexists with a selected direct stage snapshot on Nov 1;
-- XML-only totals on Nov 10 must not fabricate a stage-onset candidate.
INSERT INTO metric_points VALUES
 ('2026-11-01 00:00:00 +0100','watch','sleep_total','hr',8,'ok','apple_health_xml',101),
 ('2026-11-01 00:00:00 +0100','watch','sleep_core','hr',8,'ok','live',NULL),
 ('2026-11-10 00:00:00 +0100','watch','sleep_total','hr',8,'ok','apple_health_xml',102),
 ('2026-11-10 00:00:00 +0100','watch','sleep_core','hr',8,'ok','apple_health_xml',102);

INSERT INTO workouts VALUES
 ('2026-10-31 21:00+01','2026-10-31 21:30+01'),
 ('2026-11-01 21:00+01','2026-11-01 21:30+01'),
 ('2026-11-02 21:00+01','2026-11-02 21:30+01'),
 ('2026-11-03 21:00+01','2026-11-03 21:30+01'),
 ('2026-11-04 21:00+01','2026-11-04 21:30+01'),
 ('2026-11-05 21:00+01','2026-11-05 21:30+01'),
 ('2026-11-06 21:00+01','2026-11-06 21:30+01'),
 ('2026-11-07 21:00+01','2026-11-07 21:30+01'),
 ('2026-11-08 21:00+01','2026-11-08 21:30+01');
INSERT INTO workouts VALUES ('2026-11-10 21:00+01','2026-11-10 21:30+01'),('2026-11-11 21:00+01','2026-11-11 21:30+01');

INSERT INTO subjective_checkins VALUES
 ('2026-11-01','fixture','answered','great','2026-11-01 06:30+01','2026-11-01 07:00+01','2026-11-01 08:00+01'),
 ('2026-11-02','fixture','answered','ok','2026-11-02 06:30+01','2026-11-02 07:00+01','2026-11-02 08:00+01'),
 ('2026-11-03','fixture','answered','meh','2026-11-03 06:30+01','2026-11-03 07:00+01','2026-11-03 08:00+01'),
 ('2026-11-04','fixture','answered','great','2026-11-04 06:30+01','2026-11-04 07:00+01','2026-11-04 08:00+01'),
 ('2026-11-05','fixture','answered','great','2026-11-05 06:30+01','2026-11-05 07:00+01','2026-11-05 08:00+01'),
 ('2026-11-06','fixture','answered','great','2026-11-06 06:30+01','2026-11-06 07:00+01','2026-11-06 08:00+01'),
 ('2026-11-07','fixture','answered','great','2026-11-07 06:30+01','2026-11-07 07:00+01','2026-11-07 08:00+01'),
 ('2026-11-08','fixture','answered','great','2026-11-08 06:30+01','2026-11-08 07:00+01','2026-11-08 08:00+01'),
 ('2026-11-09','fixture','answered','sick','2026-11-09 05:30+01','2026-11-09 05:45+01','2026-11-09 08:00+01'),
 ('2026-11-11','fixture','answered','great','2026-11-11 06:30+01','2026-11-11 07:00+01','2026-11-11 08:00+01'),
 ('2026-11-12','fixture','answered','great','2026-11-12 06:30+01','2026-11-12 07:00+01','2026-11-12 08:00+01');
