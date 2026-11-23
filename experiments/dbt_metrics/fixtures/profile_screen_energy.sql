-- Installed only after the dated section probes, under one disposable profile login.
SET TIME ZONE 'UTC';
INSERT INTO settings(key,value) VALUES ('timezone','UTC') ON CONFLICT(key) DO UPDATE SET value=excluded.value;
ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS energy_capacity integer,
                         ADD COLUMN IF NOT EXISTS energy_eod_current integer,
                         ADD COLUMN IF NOT EXISTS energy_drain integer,
                         ADD COLUMN IF NOT EXISTS energy_verdict text;
ALTER TABLE energy_snapshots ADD COLUMN IF NOT EXISTS computed_at timestamptz;
INSERT INTO calendar SELECT (current_date - n)::text FROM generate_series(0,14) n ON CONFLICT DO NOTHING;
INSERT INTO metric_points(metric_name,date,source,qty,quality)
SELECT s.metric,(current_date-n)::text || ' 00:00:00 +0000','Energy Screen Apple Watch',s.qty,'ok'
FROM generate_series(0,14) n CROSS JOIN LATERAL (VALUES
    ('heart_rate_variability',case when n=0 then 75 else 60 + n % 4 end),
    ('resting_heart_rate',60), ('sleep_total',8), ('sleep_core',5), ('sleep_deep',1),
    ('sleep_rem',2), ('sleep_awake',0.4), ('step_count',4000), ('active_energy',300)
) s(metric,qty);

-- The historical HRV baseline deliberately disagrees with the raw-point graph.
-- Compatibility history comes from recorded daily_scores in both reader modes.
INSERT INTO daily_scores(date,hrv_avg,rhr_avg,sleep_total,sleep_core,sleep_deep,sleep_rem,sleep_awake,steps,calories,
                         energy_capacity,energy_eod_current,energy_drain,energy_verdict)
SELECT (current_date-n)::text,case when n=0 then 75 else 65 end,60,8,5,1,2,0.4,4000,300,
       80-n+profile_offset,70-n+profile_offset,10,'moderate'
FROM generate_series(0,14) n CROSS JOIN (select case when current_schema()='peer_source' then 5 else 0 end as profile_offset) p
ON CONFLICT(date) DO UPDATE SET hrv_avg=excluded.hrv_avg,rhr_avg=excluded.rhr_avg,sleep_total=excluded.sleep_total,
    sleep_core=excluded.sleep_core,sleep_deep=excluded.sleep_deep,sleep_rem=excluded.sleep_rem,
    sleep_awake=excluded.sleep_awake,steps=excluded.steps,calories=excluded.calories,energy_capacity=excluded.energy_capacity,
    energy_eod_current=excluded.energy_eod_current,energy_drain=excluded.energy_drain,energy_verdict=excluded.energy_verdict;
INSERT INTO hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count)
SELECT metric_name,substring(date,1,13) || ':00',source,qty,qty,qty,1 FROM metric_points
WHERE source='Energy Screen Apple Watch' AND substring(date,1,10)=current_date::text;
-- The most recent measured bucket is authoritative even when raw HRV is corrected.
INSERT INTO energy_snapshots(ts_bucket,date,bank,drain_delta,restore_delta,formula_version,flags,computed_at)
SELECT current_date::timestamptz + minutes * interval '1 minute',current_date::text,
       case when minutes=5 then 99 when current_schema()='peer_source' then 84 else 76 end,
       6,2,3,'{}',current_date::timestamptz + minutes * interval '1 minute'
FROM (values (5),(10)) b(minutes);
