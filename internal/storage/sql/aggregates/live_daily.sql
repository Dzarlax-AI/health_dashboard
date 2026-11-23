WITH days AS (SELECT $1::text as date), daily_steps AS ({{DAILY_STEP_TOTALS}}), per_source AS (
    {{DAILY_DATE_SOURCES}}
),
{{SLEEP_SELECTION_CTES}},
daily_picks AS (
    SELECT $1::text as date,(SELECT src FROM sleep_picked) as picked_source,(SELECT ok FROM sleep_picked_complete) as complete
), candidates AS (
    {{DAILY_CANDIDATES}}
)
INSERT INTO daily_scores
    (date,hrv_avg,rhr_avg,sleep_total,sleep_deep,sleep_rem,sleep_core,sleep_awake,
     sleep_unspecified,steps,calories,exercise_min,spo2_avg,vo2_avg,resp_avg,computed_at)
SELECT candidates.*,NOW()::text FROM candidates
ON CONFLICT(date) DO UPDATE SET
    (hrv_avg,rhr_avg,sleep_total,sleep_deep,sleep_rem,sleep_core,sleep_awake,sleep_unspecified,
     steps,calories,exercise_min,spo2_avg,vo2_avg,resp_avg) = (SELECT {{DAILY_RETAINED}}),
    computed_at=excluded.computed_at
