-- psql variables: start_date, end_date (inclusive wake dates), tz (IANA).
-- Run inside an already authorized, single-tenant REPEATABLE READ READ ONLY transaction.
-- Aggregate output only. No persistent objects or row-level results.
\set ON_ERROR_STOP on
SELECT 1 / ((current_setting('transaction_read_only') = 'on'
 AND current_setting('transaction_isolation') = 'repeatable read'
 AND current_schema() IS NOT NULL
 AND current_user = (SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname = current_schema())
 AND :'start_date'::date <= :'end_date'::date
 AND EXISTS (SELECT FROM pg_timezone_names WHERE name = :'tz'))::int) AS guard \gset
WITH calendar AS (
 SELECT d::date AS day FROM generate_series(:'start_date'::date, :'end_date'::date, interval '1 day') d
), nights AS (
 SELECT c.day, n.source, n.source_epoch, n.duration_hours, n.coverage_generation,
        n.capture_completeness, n.duration_assessment, n.finalization_state, n.claim_eligibility
 FROM calendar c LEFT JOIN completed_night_sleep n ON n.wake_date=c.day::text
), episodes AS (
 SELECT n.*, e.start_at, e.end_at,
        count(e.episode_id) OVER (PARTITION BY n.day) AS episode_count,
        row_number() OVER (PARTITION BY n.day ORDER BY e.start_at NULLS LAST, e.episode_id) AS episode_rank,
        p.capture_completeness AS period_completeness, p.source_epoch AS period_epoch,
        p.coverage_generation AS period_generation, p.covered_interval_start, p.covered_interval_end,
        e.source AS episode_source, e.source_epoch AS episode_epoch, e.coverage_generation AS episode_generation,
        e.capture_state, e.duration_assessment AS episode_assessment
 FROM nights n LEFT JOIN sleep_period_coverage p ON p.wake_date=n.day::text
 LEFT JOIN completed_sleep_episode e ON e.wake_date=n.day::text
), starts AS (
 SELECT *, (episode_count=1 AND capture_completeness='complete' AND duration_assessment='plausible'
   AND finalization_state='final' AND claim_eligibility='eligible'
   AND period_completeness='complete' AND capture_state='complete' AND episode_assessment='plausible'
   AND source <> '' AND source_epoch <> '' AND source=episode_source AND source_epoch=episode_epoch
   AND source_epoch=period_epoch AND coverage_generation IS NOT NULL AND coverage_generation <> ''
   AND coverage_generation=period_generation AND period_generation=episode_generation
   AND start_at >= covered_interval_start AND end_at <= covered_interval_end AND end_at > start_at
   AND abs(extract(epoch FROM (end_at-start_at))-duration_hours*3600) <= 1
   AND (end_at AT TIME ZONE :'tz')::date=day) IS TRUE AS reliable
 FROM episodes WHERE episode_rank=1
), joined AS (
 SELECT s.*, w.start_time AS workout_start, w.end_time AS workout_end,
   EXISTS (SELECT FROM subjective_checkins k WHERE k.date=s.day::text AND k.status IN ('answered','late_answered')
      AND k.answer IS NOT NULL AND k.answered_at >= s.end_at
      AND (k.answered_at AT TIME ZONE :'tz')::date=s.day) AS answered,
   EXISTS (SELECT FROM subjective_checkins k WHERE k.date=s.day::text AND k.status='answered'
      AND k.answer IS NOT NULL AND k.answered_at >= s.end_at AND k.answered_at < k.expires_at
      AND (k.answered_at AT TIME ZONE :'tz')::date=s.day) AS on_time_answered
 FROM starts s LEFT JOIN LATERAL (
   SELECT start_time,end_time FROM workouts
   WHERE s.reliable AND start_time < s.start_at AND end_time >= s.start_at - interval '24 hours'
     AND end_time > start_time
   ORDER BY end_time DESC, start_time DESC LIMIT 1
 ) w ON true
), flags AS (
 SELECT *, (reliable AND workout_end IS NOT NULL AND workout_end <= start_at) AS workout_ok,
   extract(epoch FROM (start_at-workout_end))/3600.0 AS interval_hours
 FROM joined
), epochs AS (
 SELECT source, source_epoch, count(*) AS nights,
   min(interval_hours) AS min_hours, max(interval_hours) AS max_hours,
   avg(interval_hours) AS mean_hours, stddev_samp(interval_hours) AS stddev_hours
 FROM flags WHERE workout_ok AND answered GROUP BY source,source_epoch
), epoch_output AS (
 SELECT row_number() OVER (ORDER BY source,source_epoch) AS epoch_number,
        nights, CASE WHEN nights >= 5 THEN min_hours END AS min_hours,
        CASE WHEN nights >= 5 THEN max_hours END AS max_hours,
        CASE WHEN nights >= 5 THEN mean_hours END AS mean_hours,
        CASE WHEN nights >= 5 THEN stddev_hours END AS stddev_hours FROM epochs
)
SELECT json_build_object(
 'start_date', :'start_date', 'end_date', :'end_date', 'timezone', :'tz',
 'calendar_days', count(*), 'night_records', count(*) FILTER (WHERE source IS NOT NULL),
 'supported_onset_proxies', count(*) FILTER (WHERE reliable),
 'supported_onset_and_prior_workout', count(*) FILTER (WHERE workout_ok),
 'eligible_with_answered_checkin', count(*) FILTER (WHERE workout_ok AND answered),
 'eligible_with_on_time_checkin', count(*) FILTER (WHERE workout_ok AND on_time_answered),
 'eligible_with_late_only_checkin', count(*) FILTER (WHERE workout_ok AND answered AND NOT on_time_answered),
 'missing_or_unsupported_onset', count(*) FILTER (WHERE NOT reliable),
 'no_qualifying_workout', count(*) FILTER (WHERE reliable AND NOT workout_ok),
 'no_subsequent_answer', count(*) FILTER (WHERE workout_ok AND NOT answered),
 'epochs', COALESCE((SELECT json_agg(row_to_json(e) ORDER BY epoch_number) FROM epoch_output e), '[]'::json)
) AS coverage FROM flags;
