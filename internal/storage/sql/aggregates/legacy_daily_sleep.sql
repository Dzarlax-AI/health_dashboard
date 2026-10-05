WITH per_source AS (
    SELECT SUBSTRING(hour,1,10) AS day, metric_name, source,
           SUM(avg_val) AS sum_val
    FROM hourly_metrics
    WHERE metric_name IN ({{SLEEP_METRICS}})
      {{FROM_CLAUSE}}
    GROUP BY SUBSTRING(hour,1,10), metric_name, source
),
sleep_total_per_day AS (
    SELECT day, source, sum_val FROM per_source WHERE metric_name = 'sleep_total'
),
day_stats AS (
    SELECT day,
           COUNT(*)        AS n_sources,
           MIN(sum_val)    AS min_total,
           MAX(sum_val)    AS max_total
    FROM sleep_total_per_day
    GROUP BY day
),
-- Both pickers add source ASC as a stable tiebreaker so equal totals
-- always resolve to the same row across reruns; without it, DISTINCT ON
-- can return either matching row and daily_scores would drift.
priority_pick AS (
    SELECT DISTINCT ON (day) day, source
    FROM sleep_total_per_day
    ORDER BY day,
        {{SLEEP_PRIORITY_CASE}},
        sum_val DESC,
        source ASC
),
min_pick AS (
    SELECT DISTINCT ON (day) day, source
    FROM sleep_total_per_day
    ORDER BY day, sum_val ASC, source ASC
),
sleep_picked AS (
    SELECT s.day,
        CASE WHEN s.n_sources > 1 AND s.min_total > {{SLEEP_MIN_HOURS}}
                  AND s.max_total > s.min_total * {{SLEEP_DIVERGENCE}}
             THEN m.source
             ELSE p.source
        END AS src
    FROM day_stats s
    LEFT JOIN priority_pick p ON p.day = s.day
    LEFT JOIN min_pick      m ON m.day = s.day
),
-- Atomicity gate: when MULTIPLE sources contributed sleep_total for the
-- night, demand the picked source has all five stages so we don't write
-- a NULL stage that ON CONFLICT COALESCE then fills from a prior row
-- with a DIFFERENT source (the mixing bug PR #26 closed). With a single
-- source there is no mixing risk, so trust it as-is — strict completeness
-- would erase a real night just because one stage happened to be 0
-- (HAE Apple Watch nights with sleep_awake = 0 hit this: hourly_metrics
-- filter qty > 0 drops the awake row, so the source has only 4 of 5
-- stages even though the night is fully recorded).
sleep_complete AS (
    -- Conditional gate (mirrors upsertDailyForDate):
    --   single source                              → trust as-is
    --   multi-source + all 5 stages from picked    → complete (stage-tracking device)
    --   multi-source + total + unspecified picked  → complete (coarse-only device)
    -- KNOWN LIMITATION (Issue #77): a picked source emitting ONLY sleep_total
    -- (no stages, no unspecified) fails this gate by design — see the matching
    -- comment in upsertDailyForDate's sleep_picked_complete CTE for the
    -- reasoning. Both gates must stay in lockstep on this corner.
    --
    -- Regression coverage: see sleep_gate.go::EvaluateSleepPickedComplete
    -- — pure Go twin exercised by sleep_gate_test.go.
    SELECT sp.day, sp.src, (
        (SELECT COUNT(DISTINCT source) FROM sleep_total_per_day WHERE day = sp.day) <= 1
        OR (
          SELECT COUNT(DISTINCT metric_name) FROM per_source
           WHERE day = sp.day AND source = sp.src
             AND metric_name IN ({{SLEEP_TRADITIONAL_METRICS}})
        ) = 5
        OR (
          SELECT COUNT(DISTINCT metric_name) FROM per_source
           WHERE day = sp.day AND source = sp.src
             AND metric_name IN ({{SLEEP_COARSE_METRICS}})
        ) = 2
    ) AS ok
    FROM sleep_picked sp
),
day_metric AS (
    SELECT p.day, p.metric_name,
        CASE WHEN sc.ok AND p.source = sc.src THEN p.sum_val END AS val
    FROM per_source p
    JOIN sleep_complete sc ON sc.day = p.day
)
INSERT INTO daily_scores (date, sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_awake, sleep_unspecified, computed_at)
SELECT day,
    MAX(val) FILTER (WHERE metric_name = 'sleep_total'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_deep'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_rem'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_core'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_awake'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_unspecified'),
    NOW()::TEXT
FROM day_metric
GROUP BY day
ON CONFLICT(date) DO UPDATE SET
    -- Atomic overwrite when the gate passed (EXCLUDED.sleep_total IS NOT
    -- NULL); preserve prior row as a whole otherwise. Mirrors the same
    -- pattern in upsertDailyForDate — coarse-only picks must clear
    -- stale stage columns from a prior staged-source write or we
    -- re-create the mixed-source row the gate exists to prevent
    -- (CodeRabbit PR #73).
    sleep_total       = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_total       ELSE daily_scores.sleep_total       END,
    sleep_deep        = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_deep        ELSE daily_scores.sleep_deep        END,
    sleep_rem         = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_rem         ELSE daily_scores.sleep_rem         END,
    sleep_core        = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_core        ELSE daily_scores.sleep_core        END,
    sleep_awake       = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_awake       ELSE daily_scores.sleep_awake       END,
    sleep_unspecified = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_unspecified ELSE daily_scores.sleep_unspecified END,
    computed_at       = EXCLUDED.computed_at
