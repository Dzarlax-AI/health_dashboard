-- Separate capture schema keeps the normal application-reader fixtures independent.
create schema archive_records;
create table archive_records.completed_night_sleep as
select wake_date,source,source_epoch,algorithm_version,observed_at,
       observed_at as finalized_at,null::timestamptz as covered_interval_start,
       null::timestamptz as covered_interval_end
from source_data.completed_night_sleep;

create table archive_records.workouts (
    id bigint primary key, external_id text, name text, origin text, source_snapshot_at timestamptz,
    start_time timestamptz, end_time timestamptz, received_at timestamptz, duration_sec double precision,
    is_indoor boolean, avg_hr_bpm double precision, max_hr_bpm double precision, energy_kcal double precision,
    intensity double precision,distance_km double precision,avg_speed_kmh double precision,max_speed_kmh double precision,
    elevation_up_m double precision,step_count_total integer,step_cadence_spm double precision,
    temperature_c double precision,humidity_pct double precision,
    hr_z1_sec integer,hr_z2_sec integer,hr_z3_sec integer,hr_z4_sec integer,hr_z5_sec integer
);
insert into archive_records.workouts
    (id,external_id,name,origin,start_time,end_time,received_at,duration_sec,is_indoor,step_count_total)
values (1,'sample','walk','live','2026-01-01 09:00Z','2026-01-01 09:15Z','2026-01-02 03:00Z',900,false,0);

create table archive_records.energy_snapshots (
    ts_bucket timestamptz primary key,date text,bank integer,drain_delta integer,restore_delta integer,
    formula_version integer,components jsonb,flags text[],computed_at timestamptz
);
insert into archive_records.energy_snapshots values
    ('2026-01-01 10:00Z','2026-01-01',0,-1,0,3,'{"hr":1}','{}','2026-01-01 10:01Z');

create table archive_records.target_snapshots (
    date text,sub_score text,target_kind text,source_epoch text,data_coverage jsonb,
    formula_version integer,computed_at timestamptz,primary key(date,sub_score,target_kind)
);
insert into archive_records.target_snapshots values
    ('2026-01-01','recovery_stability','rolling_3d','first','{"n":0}',2,'2026-01-04 19:00Z');

create table archive_records.feature_snapshots (
    date text, sub_score text, features jsonb, source_epoch text,
    feature_version integer, computed_at timestamptz, primary key(date, sub_score)
);
insert into archive_records.feature_snapshots values
    ('2026-01-01','recovery_stability','{"sleep_eff_mean_7d":0,"sleep_capture_low":false}','first',1,'2026-01-02 09:00Z'),
    ('2026-01-01','passive_efficiency','{"prev_walking_hr":null}','first',1,'2026-01-02 09:00Z'),
    ('2026-01-01','acute_risk','{"warmup_met":false}','first',1,'2026-01-02 09:00Z'),
    ('2026-01-01','chronic_load','{"paired_count_to_t":0}','first',1,'2026-01-02 09:00Z');

create table archive_records.daily_insight_bundles (
    date text, lang text, decision_id text, schema_version text, snapshot jsonb, primary key(date, lang)
);
insert into archive_records.daily_insight_bundles values
    ('2026-01-01','en','example','v1','{"evidence":[{"id":"example","domain":"sleep","value":0}]}');

create table archive_records.naive_baselines (
    date text, sub_score text, target_kind text, baseline_kind text,
    source_epoch text, formula_version integer, computed_at timestamptz
);
insert into archive_records.naive_baselines values
    ('2026-01-01','acute_risk','daily','mean','first',1,'2026-01-02 09:00Z');

create table archive_records.chip_calibrations (
    sub_score text, target_kind text, source_epoch text, method text,
    calibration_window_days integer, computed_at timestamptz
);
insert into archive_records.chip_calibrations values
    ('acute_risk','daily','first','percentile',45,'2026-01-02 09:00Z');

create table archive_records.sleep_goal (
    effective_date text, goal_hours float8, version text, updated_at timestamptz
);
insert into archive_records.sleep_goal values ('2026-01-01',8,'v1','2026-01-01 08:00Z');

create table archive_records.derived_metrics (
    metric_date date, metric_name text, value_type text, state text, inputs_hash text,
    value_timestamp timestamptz, calculated_at timestamptz, formula_version text
);
insert into archive_records.derived_metrics values
    ('2026-01-01','wake_time','timestamp','provisional','sample','2026-01-01 08:00Z','2026-01-01 08:15Z','v1');

create table archive_records.daily_scores (
    date text, energy_capacity integer, energy_eod_current integer, energy_drain integer, energy_verdict text
);
insert into archive_records.daily_scores values ('2026-01-01',null,0,0,'rest');
insert into archive_records.energy_snapshots values
    ('2026-01-01 23:55Z','2026-01-01',50,0,0,3,'{}','{backfilled}','2026-01-02 03:00Z');

alter table archive_records.completed_night_sleep add diagnostics jsonb default '{}';
create table archive_records.report_history (
    id uuid primary key, revision integer, kind text, local_date text, language text,
    format_revision text, prepared_at timestamptz, context jsonb
);
insert into archive_records.report_history values
    ('00000000-0000-0000-0000-000000000001',1,'morning','2026-01-01','en','v1','2026-01-01 09:00Z',
     '{"sleep":{"date":"2026-01-01","hours":7,"recent_recorded_nights":0,"capture":"complete"}}');
