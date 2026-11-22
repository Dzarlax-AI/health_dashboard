CREATE TABLE completed_night_sleep (
  wake_date text, source text, source_epoch text, duration_hours double precision,
  coverage_generation text, capture_completeness text, duration_assessment text,
  finalization_state text, claim_eligibility text
);
CREATE TABLE sleep_period_coverage (
  wake_date text, capture_completeness text, source_epoch text,
  coverage_generation text, covered_interval_start timestamptz, covered_interval_end timestamptz
);
CREATE TABLE completed_sleep_episode (
  episode_id text, wake_date text, start_at timestamptz, end_at timestamptz,
  source text, source_epoch text, coverage_generation text, capture_state text,
  duration_assessment text
);
CREATE TABLE workouts (start_time timestamptz, end_time timestamptz);
CREATE TABLE subjective_checkins (date text, status text, answer text, answered_at timestamptz, expires_at timestamptz);

-- 2026-06-02 Belgrade midnight boundary: the event is still June 1 in UTC.
INSERT INTO completed_night_sleep VALUES
 ('2026-06-02','watch','epoch-a',7,'gen-a','complete','plausible','final','eligible'),
 ('2026-06-03','watch','epoch-a',7,'gen-a','complete','plausible','final','eligible'),
 ('2026-06-04','watch','epoch-a',7,'gen-a','complete','plausible','final','eligible'),
 ('2026-06-05','watch','epoch-a',7,'gen-a','complete','plausible','final','eligible'),
 ('2026-06-06','watch','epoch-a',7,'gen-a','complete','plausible','final','eligible'),
 ('2026-06-07','watch','epoch-a',7,'gen-a','complete','plausible','final','eligible'),
 ('2026-06-08','watch','epoch-a',7,'gen-a','complete','plausible','final','eligible'),
 ('2026-06-09','watch','epoch-a',7,'gen-a','complete','plausible','final','eligible');

INSERT INTO sleep_period_coverage VALUES
 ('2026-06-02','complete','epoch-a','gen-a','2026-06-01 21:00+00','2026-06-02 06:30+00'),
 ('2026-06-03','complete','epoch-a','gen-a','2026-06-02 21:00+00','2026-06-03 06:30+00'),
 ('2026-06-04','complete','epoch-a','gen-a','2026-06-03 21:00+00','2026-06-04 06:30+00'),
 ('2026-06-05','complete','epoch-a','gen-a','2026-06-04 21:00+00','2026-06-05 06:30+00'),
 ('2026-06-06','complete','epoch-a','gen-a','2026-06-05 21:00+00','2026-06-06 06:30+00'),
 ('2026-06-07','complete','epoch-a','gen-a','2026-06-06 21:00+00','2026-06-07 06:30+00'),
 ('2026-06-08','complete','epoch-a','gen-a','2026-06-07 21:00+00','2026-06-08 06:30+00'),
 ('2026-06-09','complete','epoch-a','gen-a','2026-06-08 21:00+00','2026-06-09 06:30+00');

INSERT INTO completed_sleep_episode VALUES
 ('valid','2026-06-02','2026-06-01 22:30+00','2026-06-02 05:30+00','watch','epoch-a','gen-a','complete','plausible'),
 ('overlap-workout','2026-06-03','2026-06-02 22:30+00','2026-06-03 05:30+00','watch','epoch-a','gen-a','complete','plausible'),
 ('device-mismatch','2026-06-04','2026-06-03 22:30+00','2026-06-04 05:30+00','phone','epoch-a','gen-a','complete','plausible'),
 ('duplicate-a','2026-06-05','2026-06-04 22:30+00','2026-06-05 05:30+00','watch','epoch-a','gen-a','complete','plausible'),
 ('duplicate-b','2026-06-05','2026-06-04 23:00+00','2026-06-05 05:30+00','watch','epoch-a','gen-a','complete','plausible'),
 ('missing-start','2026-06-06',NULL,'2026-06-06 05:30+00','watch','epoch-a','gen-a','complete','plausible'),
 ('no-answer','2026-06-07','2026-06-06 22:30+00','2026-06-07 05:30+00','watch','epoch-a','gen-a','complete','plausible'),
 ('late-answer','2026-06-08','2026-06-07 22:30+00','2026-06-08 05:30+00','watch','epoch-a','gen-a','complete','plausible'),
 ('bad-answer','2026-06-09','2026-06-08 22:30+00','2026-06-09 05:30+00','watch','epoch-a','gen-a','complete','plausible');

INSERT INTO workouts VALUES
 ('2026-06-01 21:30+00','2026-06-01 22:00+00'), -- valid; 30m before sleep
 ('2026-06-02 22:00+00','2026-06-02 23:00+00'), -- overlaps next sleep start
 ('2026-06-03 21:30+00','2026-06-03 22:00+00'), -- same timestamp pattern, wrong sleep device
 ('2026-06-04 21:30+00','2026-06-04 22:00+00'), -- duplicate episodes
 ('2026-06-05 21:30+00','2026-06-05 22:00+00'), -- missing sleep start
 ('2026-06-06 21:30+00','2026-06-06 22:00+00'),
 ('2026-06-07 21:30+00','2026-06-07 22:00+00'),
 ('2026-06-08 21:30+00','2026-06-08 22:00+00');

INSERT INTO subjective_checkins VALUES
 ('2026-06-02','answered','good','2026-06-02 06:00+00','2026-06-02 07:00+00'), -- same Belgrade day, after sleep
 ('2026-06-03','answered','good','2026-06-03 06:00+00','2026-06-03 07:00+00'),
 ('2026-06-04','answered','good','2026-06-04 06:00+00','2026-06-04 07:00+00'),
 ('2026-06-05','answered','good','2026-06-05 06:00+00','2026-06-05 07:00+00'),
 ('2026-06-06','answered','good','2026-06-06 06:00+00','2026-06-06 07:00+00'),
 ('2026-06-07','answered','good','2026-06-07 05:00+00','2026-06-07 07:00+00'), -- before episode end
 ('2026-06-08','late_answered','good','2026-06-08 08:00+00','2026-06-08 07:00+00'),
 ('2026-06-09','answered',NULL,'2026-06-09 06:00+00','2026-06-09 07:00+00'); -- invalid answer value
