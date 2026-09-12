-- Read-only baseline for the evidence-grounded sleep-insight plan.
--
-- Run only through an approved production read path. This file contains no
-- credentials and performs no writes. Set primary_source to the source chosen
-- by the existing server source-priority rule before running:
--
--   psql -X -v ON_ERROR_STOP=1 -v primary_source='preferred source label' \
--     -f docs/ai-plans/2026-09-11-sleep-insight-baseline.sql
--
-- This is an exploratory proxy, not a completed_night_sleep canonicalizer.
-- quality=ok excludes hard-impossible values only; it is not capture proof.
--
-- Historical-policy note: sections 2 and 2b reproduce the former strict
-- action policy (14 preceding known-false days). That policy was retired from
-- the plan after this replay showed its availability was fragile under missing
-- data. Keep the query and output only as a reproducible baseline; do not use
-- eligible_action_events as a forecast of the current seven-day cadence.

-- 1. Explain the daily proxy grain and data-quality exclusions.
WITH per_source AS (
  SELECT
    substring(date, 1, 10)::date AS day,
    source,
    count(*) AS rows_per_day,
    sum(qty) AS sleep_hours
  FROM health.metric_points
  WHERE metric_name = 'night_sleep_total'
    AND quality = 'ok'
  GROUP BY 1, 2
), chosen AS (
  SELECT DISTINCT ON (day)
    day, source, rows_per_day, sleep_hours
  FROM per_source
  ORDER BY day,
    CASE WHEN source = :'primary_source' THEN 0 ELSE 1 END,
    sleep_hours DESC
)
SELECT
  source,
  count(*) AS days,
  count(*) FILTER (WHERE rows_per_day > 1) AS multi_row_days,
  count(*) FILTER (WHERE sleep_hours < 3 OR sleep_hours > 14) AS implausible_sum_days,
  round(percentile_cont(0.5) WITHIN GROUP (ORDER BY sleep_hours)::numeric, 2) AS median_hours
FROM chosen
GROUP BY source
ORDER BY days DESC;

-- 2. Replay B0 availability. The output counts explain the 210 → 180 → 118
-- denominator transition. `true` and `false` are proxy states only.
WITH per_source AS (
  SELECT substring(date, 1, 10)::date AS day, source, sum(qty) AS sleep_hours
  FROM health.metric_points
  WHERE metric_name = 'night_sleep_total'
    AND quality = 'ok'
  GROUP BY 1, 2
), chosen AS (
  SELECT DISTINCT ON (day) day, sleep_hours
  FROM per_source
  ORDER BY day,
    CASE WHEN source = :'primary_source' THEN 0 ELSE 1 END,
    sleep_hours DESC
), bounds AS (
  SELECT min(day) AS first_day, max(day) AS last_day FROM chosen
), calendar AS (
  SELECT generate_series(first_day + 93, last_day, interval '1 day')::date AS day
  FROM bounds
), evaluated AS (
  SELECT
    c.day,
    (SELECT count(*)
     FROM chosen n
     WHERE n.day BETWEEN c.day - 93 AND c.day - 4
       AND n.sleep_hours BETWEEN 3 AND 14) AS reference_n,
    (SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY n.sleep_hours)
     FROM chosen n
     WHERE n.day BETWEEN c.day - 93 AND c.day - 4
       AND n.sleep_hours BETWEEN 3 AND 14) AS reference_hours,
    (SELECT count(*)
     FROM chosen n
     WHERE n.day BETWEEN c.day - 3 AND c.day
       AND n.sleep_hours BETWEEN 3 AND 14) AS current_n,
    (SELECT count(*)
     FROM chosen n
     WHERE n.day BETWEEN c.day - 3 AND c.day
       AND n.sleep_hours BETWEEN 3 AND 14
       AND n.sleep_hours <= (
         SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY x.sleep_hours)
         FROM chosen x
         WHERE x.day BETWEEN c.day - 93 AND c.day - 4
           AND x.sleep_hours BETWEEN 3 AND 14
       ) - 0.5) AS short_n
  FROM calendar c
), states AS (
  SELECT
    day,
    CASE
      WHEN reference_n < 60 OR current_n < 4 THEN 'unknown'
      WHEN short_n >= 3 THEN 'true'
      ELSE 'false'
    END AS state
  FROM evaluated
), action_events AS (
  SELECT s.day
  FROM states s
  WHERE s.state = 'true'
    AND (
      SELECT count(*)
      FROM states p
      WHERE p.day BETWEEN s.day - 14 AND s.day - 1
        AND p.state = 'false'
    ) = 14
)
SELECT
  (SELECT count(*) FROM chosen) AS source_days,
  (SELECT count(*) FROM chosen WHERE sleep_hours BETWEEN 3 AND 14) AS plausible_proxy_days,
  (SELECT count(*) FROM states) AS evaluable_calendar_days,
  count(*) FILTER (WHERE state = 'true') AS true_claim_days,
  count(*) FILTER (WHERE state = 'false') AS false_days,
  count(*) FILTER (WHERE state = 'unknown') AS unknown_days,
  (SELECT count(*) FROM action_events) AS eligible_action_events
FROM states;

-- 2b. Explain why a current true did not get an action under the retired
-- strict policy. This separates a prior-true policy block from an unknown-data
-- block; neither classification applies to the current seven-day cadence.
WITH per_source AS (
  SELECT substring(date, 1, 10)::date AS day, source, sum(qty) AS sleep_hours
  FROM health.metric_points
  WHERE metric_name = 'night_sleep_total'
    AND quality = 'ok'
  GROUP BY 1, 2
), chosen AS (
  SELECT DISTINCT ON (day) day, sleep_hours
  FROM per_source
  ORDER BY day,
    CASE WHEN source = :'primary_source' THEN 0 ELSE 1 END,
    sleep_hours DESC
), bounds AS (
  SELECT min(day) AS first_day, max(day) AS last_day FROM chosen
), calendar AS (
  SELECT generate_series(first_day + 93, last_day, interval '1 day')::date AS day
  FROM bounds
), evaluated AS (
  SELECT
    c.day,
    (SELECT count(*) FROM chosen n
     WHERE n.day BETWEEN c.day - 93 AND c.day - 4 AND n.sleep_hours BETWEEN 3 AND 14) AS reference_n,
    (SELECT count(*) FROM chosen n
     WHERE n.day BETWEEN c.day - 3 AND c.day AND n.sleep_hours BETWEEN 3 AND 14) AS current_n,
    (SELECT count(*) FROM chosen n
     WHERE n.day BETWEEN c.day - 3 AND c.day
       AND n.sleep_hours BETWEEN 3 AND 14
       AND n.sleep_hours <= (
         SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY x.sleep_hours)
         FROM chosen x
         WHERE x.day BETWEEN c.day - 93 AND c.day - 4 AND x.sleep_hours BETWEEN 3 AND 14
       ) - 0.5) AS short_n
  FROM calendar c
), states AS (
  SELECT day, CASE
    WHEN reference_n < 60 OR current_n < 4 THEN 'unknown'
    WHEN short_n >= 3 THEN 'true'
    ELSE 'false'
  END AS state
  FROM evaluated
), blockers AS (
  SELECT
    s.day,
    (SELECT count(*) FROM states p WHERE p.day BETWEEN s.day - 14 AND s.day - 1) AS prior_rows,
    (SELECT count(*) FROM states p WHERE p.day BETWEEN s.day - 14 AND s.day - 1 AND p.state = 'unknown') AS unknown_prior,
    (SELECT count(*) FROM states p WHERE p.day BETWEEN s.day - 14 AND s.day - 1 AND p.state = 'true') AS true_prior
  FROM states s
  WHERE s.state = 'true'
)
SELECT
  CASE
    WHEN prior_rows < 14 THEN 'pre-evaluation-history'
    WHEN unknown_prior > 0 AND true_prior > 0 THEN 'unknown-and-prior-true'
    WHEN unknown_prior > 0 THEN 'unknown'
    WHEN true_prior > 0 THEN 'prior-true'
    ELSE 'eligible'
  END AS action_blocker,
  count(*) AS current_true_days
FROM blockers
GROUP BY 1
ORDER BY 1;

-- 3. Exploratory pairing with an outcome recorded before the report trigger.
-- `answered` is timely; `late_answered` and `expired` are deliberately excluded.
WITH per_source AS (
  SELECT substring(date, 1, 10)::date AS day, source, sum(qty) AS sleep_hours
  FROM health.metric_points
  WHERE metric_name = 'night_sleep_total'
    AND quality = 'ok'
  GROUP BY 1, 2
), chosen AS (
  SELECT DISTINCT ON (day) day, sleep_hours
  FROM per_source
  ORDER BY day,
    CASE WHEN source = :'primary_source' THEN 0 ELSE 1 END,
    sleep_hours DESC
), bounds AS (
  SELECT min(day) AS first_day, max(day) AS last_day FROM chosen
), calendar AS (
  SELECT generate_series(first_day + 93, last_day, interval '1 day')::date AS day
  FROM bounds
), evaluated AS (
  SELECT
    c.day,
    (SELECT count(*) FROM chosen n
     WHERE n.day BETWEEN c.day - 93 AND c.day - 4 AND n.sleep_hours BETWEEN 3 AND 14) AS reference_n,
    (SELECT count(*) FROM chosen n
     WHERE n.day BETWEEN c.day - 3 AND c.day AND n.sleep_hours BETWEEN 3 AND 14) AS current_n,
    (SELECT count(*) FROM chosen n
     WHERE n.day BETWEEN c.day - 3 AND c.day
       AND n.sleep_hours BETWEEN 3 AND 14
       AND n.sleep_hours <= (
         SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY x.sleep_hours)
         FROM chosen x
         WHERE x.day BETWEEN c.day - 93 AND c.day - 4 AND x.sleep_hours BETWEEN 3 AND 14
       ) - 0.5) AS short_n
  FROM calendar c
), states AS (
  SELECT day, CASE
    WHEN reference_n < 60 OR current_n < 4 THEN 'unknown'
    WHEN short_n >= 3 THEN 'true'
    ELSE 'false'
  END AS state
  FROM evaluated
), paired AS (
  SELECT
    s.state,
    CASE sc.answer
      WHEN 'sick' THEN 1
      WHEN 'meh' THEN 2
      WHEN 'ok' THEN 3
      WHEN 'great' THEN 4
    END AS wellbeing
  FROM states s
  JOIN health.subjective_checkins sc ON sc.date = s.day::text
  WHERE sc.status = 'answered'
)
SELECT
  state,
  count(*) AS paired_days,
  count(*) FILTER (WHERE wellbeing <= 2) AS low_wellbeing_days,
  round(avg(wellbeing)::numeric, 2) AS mean_wellbeing
FROM paired
GROUP BY state
ORDER BY state;
