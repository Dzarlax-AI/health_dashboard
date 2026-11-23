CREATE TABLE IF NOT EXISTS hdbt_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    shadow_enabled boolean NOT NULL DEFAULT false,
    read_enabled boolean NOT NULL DEFAULT false,
    initialized boolean NOT NULL DEFAULT false,
    ready boolean NOT NULL DEFAULT false,
    bootstrap_cursor text NOT NULL DEFAULT '',
    bootstrap_end text NOT NULL DEFAULT '',
    formula_id text NOT NULL DEFAULT '',
    source_generation numeric(20,0),
    published_at timestamptz,
    heartbeat_at timestamptz,
    last_status text NOT NULL DEFAULT 'disabled'
);
INSERT INTO hdbt_state(singleton) VALUES(true) ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS hdbt_dirty(date text PRIMARY KEY,generation numeric(20,0) NOT NULL);
CREATE TABLE IF NOT EXISTS hdbt_step_repairs(
    point_date text NOT NULL, source text NOT NULL,generation numeric(20,0) NOT NULL,
    PRIMARY KEY(point_date,source)
);
CREATE TABLE IF NOT EXISTS hdbt_hourly_metrics(
    metric_name text NOT NULL,hour text NOT NULL,source text NOT NULL,avg_val real NOT NULL,
    min_val real NOT NULL,max_val real NOT NULL,sample_count integer NOT NULL,
    PRIMARY KEY(metric_name,hour,source)
);
CREATE INDEX IF NOT EXISTS hdbt_hourly_day ON hdbt_hourly_metrics(substring(hour,1,10));
CREATE TABLE IF NOT EXISTS hdbt_daily_metrics(
    date text PRIMARY KEY, hrv_avg real,rhr_avg real,sleep_total real,sleep_deep real,sleep_rem real,
    sleep_core real,sleep_awake real,sleep_unspecified real,steps real,calories real,exercise_min real,
    spo2_avg real,vo2_avg real,resp_avg real
);
CREATE TABLE IF NOT EXISTS hdbt_step_state(
    hour text NOT NULL,source text NOT NULL,avg_val real,min_val real,max_val real,sample_count integer,
    withdrawn boolean NOT NULL,proven_input text,PRIMARY KEY(hour,source)
);
CREATE INDEX IF NOT EXISTS hdbt_step_day ON hdbt_step_state(substring(hour,1,10));
CREATE TABLE IF NOT EXISTS hdbt_step_inputs(
    metric_name text NOT NULL,date text NOT NULL,source text,qty real,quality text,
    health_record_id bigint,origin text,units text,id bigint,received_at timestamptz,source_snapshot_at timestamptz,
    PRIMARY KEY(id)
);
CREATE INDEX IF NOT EXISTS hdbt_step_input_day ON hdbt_step_inputs(substring(date,1,10));

-- Fixed, non-serving workspace. A restarted worker replaces its contents from a single source snapshot.
CREATE TABLE IF NOT EXISTS hdbt_i_calendar(date text PRIMARY KEY);
CREATE TABLE IF NOT EXISTS hdbt_i_publish(date text PRIMARY KEY);
CREATE TABLE IF NOT EXISTS hdbt_i_points(LIKE hdbt_step_inputs INCLUDING INDEXES);
CREATE TABLE IF NOT EXISTS hdbt_i_records(id bigint PRIMARY KEY,content_type text);
CREATE TABLE IF NOT EXISTS hdbt_i_repairs(point_date text,source text,PRIMARY KEY(point_date,source));
CREATE TABLE IF NOT EXISTS hdbt_w_points(LIKE hdbt_step_inputs INCLUDING INDEXES);
CREATE TABLE IF NOT EXISTS hdbt_w_step(LIKE hdbt_step_state INCLUDING INDEXES);
CREATE TABLE IF NOT EXISTS hdbt_w_hourly(LIKE hdbt_hourly_metrics INCLUDING INDEXES);
CREATE TABLE IF NOT EXISTS hdbt_w_daily(LIKE hdbt_daily_metrics INCLUDING INDEXES);
CREATE TABLE IF NOT EXISTS hdbt_backfill(date text PRIMARY KEY);
ALTER TABLE hdbt_state ADD COLUMN IF NOT EXISTS enrollment_epoch bigint NOT NULL DEFAULT 0;
CREATE OR REPLACE FUNCTION hdbt_reset_enrollment() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $hdbt$
BEGIN
    NEW.enrollment_epoch := OLD.enrollment_epoch + 1;
    NEW.initialized := false;
    NEW.ready := false;
    NEW.read_enabled := false;
    NEW.bootstrap_cursor := '';
    NEW.bootstrap_end := '';
    NEW.formula_id := '';
    NEW.source_generation := NULL;
    NEW.published_at := NULL;
    NEW.last_status := CASE WHEN NEW.shadow_enabled THEN 'enrolled' ELSE 'disabled' END;
    RETURN NEW;
END $hdbt$;
REVOKE ALL ON FUNCTION hdbt_reset_enrollment() FROM PUBLIC;
CREATE OR REPLACE TRIGGER hdbt_enrollment_change BEFORE UPDATE OF shadow_enabled ON hdbt_state
    FOR EACH ROW WHEN (OLD.shadow_enabled IS DISTINCT FROM NEW.shadow_enabled)
    EXECUTE FUNCTION hdbt_reset_enrollment();
CREATE TABLE IF NOT EXISTS hdbt_work_lease(singleton boolean PRIMARY KEY CHECK(singleton), attempt uuid NOT NULL);
