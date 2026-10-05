-- Owned disposable database only; runner verifies the container and marker.
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA source_data;
CREATE SCHEMA go_oracle;
CREATE ROLE pilot_dbt LOGIN PASSWORD 'synthetic-only';
CREATE SCHEMA dbt_output AUTHORIZATION pilot_dbt;
CREATE TABLE source_data.metric_points (
 metric_name text NOT NULL, date text NOT NULL, source text NOT NULL,
 qty real, quality text NOT NULL DEFAULT 'ok', PRIMARY KEY(metric_name,date,source)
);
CREATE INDEX idx_mp_name_day ON source_data.metric_points(metric_name,SUBSTRING(date,1,10));
CREATE INDEX idx_mp_day ON source_data.metric_points(SUBSTRING(date,1,10));
CREATE TABLE source_data.calendar(date text PRIMARY KEY);
CREATE VIEW go_oracle.metric_points AS SELECT * FROM source_data.metric_points;
CREATE TABLE go_oracle.hourly_metrics (
 metric_name text NOT NULL,hour text NOT NULL,source text NOT NULL,
 avg_val real NOT NULL,min_val real NOT NULL,max_val real NOT NULL,
 sample_count integer NOT NULL,PRIMARY KEY(metric_name,hour,source)
);
CREATE INDEX idx_hm_name_day ON go_oracle.hourly_metrics(metric_name,SUBSTRING(hour,1,10));
CREATE TABLE go_oracle.daily_scores (
 date text PRIMARY KEY,computed_at text,
 hrv_avg real,rhr_avg real,sleep_total real,sleep_deep real,sleep_rem real,
 sleep_core real,sleep_awake real,sleep_unspecified real,steps real,
 calories real,exercise_min real,spo2_avg real,vo2_avg real,resp_avg real
);
GRANT USAGE ON SCHEMA source_data TO pilot_dbt;
GRANT SELECT ON ALL TABLES IN SCHEMA source_data TO pilot_dbt;
REVOKE ALL ON SCHEMA go_oracle FROM pilot_dbt;
