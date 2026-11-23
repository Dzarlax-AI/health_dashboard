-- Run in a disposable profile's source schema before exercising product readers.
-- Jan 1 values deliberately differ from the dbt calculation in base.sql.
CREATE TABLE daily_scores (
    date text PRIMARY KEY, hrv_avg real, rhr_avg real, sleep_total real,
    sleep_deep real, sleep_rem real, sleep_core real, sleep_awake real,
    sleep_unspecified real, steps real, calories real, exercise_min real,
    spo2_avg real, vo2_avg real, resp_avg real, baseline_hr_overnight real,
    computed_at text, readiness integer, score_version integer,
    sustained_hr_load real, stress_flags text[]
);
INSERT INTO daily_scores(date,hrv_avg,rhr_avg,sleep_total,steps,baseline_hr_overnight,computed_at,readiness)
VALUES ('2026-01-01',88,111,9,901,47,'2026-01-02 06:00 UTC',77),
       ('2026-01-02',89,112,8,902,48,'2026-01-03 06:00 UTC',78);
-- A complete legacy Energy window with no matching graph observations during
-- December. Switching tests the missing-input policy as well as the bank.
INSERT INTO daily_scores(date,sleep_total,sleep_deep,sleep_rem,sleep_awake,calories,hrv_avg,rhr_avg)
SELECT d::date::text, 8, 1, 2, 0.5, 246, 60 + extract(day from d)::int % 5,
       58 + extract(day from d)::int % 3
FROM generate_series('2025-12-12'::date,'2025-12-31'::date,'1 day') d;
UPDATE daily_scores SET sleep_deep=1,sleep_rem=2,sleep_awake=0.5,calories=246
WHERE date='2026-01-01';
-- A separately recorded readiness result exercises the normal versioned
-- history cache independently from the reconstructed January sensor window.
INSERT INTO daily_scores(date,readiness,score_version) VALUES (current_date::text,73,3);
CREATE TABLE hourly_metrics (
    metric_name text, hour text, source text, avg_val real,
    min_val real, max_val real, sample_count integer,
    PRIMARY KEY(metric_name,hour,source)
);
INSERT INTO hourly_metrics VALUES ('resting_heart_rate','2026-01-01 09:00','Apple Watch',111,111,111,1),
                                  ('resting_heart_rate','2026-01-02 09:00','Apple Watch',112,112,112,1);
CREATE TABLE IF NOT EXISTS settings (key text PRIMARY KEY,value text,updated_at text);
-- No intraday Energy observation was delivered for this fixture. The Today
-- reader must distinguish this state from a failed snapshot query.
CREATE TABLE energy_snapshots (
    ts_bucket timestamptz PRIMARY KEY, date text NOT NULL, bank integer,
    drain_delta integer, restore_delta integer, formula_version integer, flags text[]
);
