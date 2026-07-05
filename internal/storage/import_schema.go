package storage

const importRunsTableDDL = `CREATE TABLE IF NOT EXISTS import_runs (
	id                BIGSERIAL PRIMARY KEY,
	origin            TEXT NOT NULL,
	source_name        TEXT NOT NULL DEFAULT '',
	started_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	finished_at        TIMESTAMPTZ,
	snapshot_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
	status            TEXT NOT NULL DEFAULT 'running',
	min_date           TEXT,
	max_date           TEXT,
	parsed_points      BIGINT NOT NULL DEFAULT 0,
	inserted_points    BIGINT NOT NULL DEFAULT 0,
	updated_points     BIGINT NOT NULL DEFAULT 0,
	deleted_points     BIGINT NOT NULL DEFAULT 0,
	skipped_points     BIGINT NOT NULL DEFAULT 0,
	parsed_workouts    BIGINT NOT NULL DEFAULT 0,
	upserted_workouts  BIGINT NOT NULL DEFAULT 0,
	deleted_workouts   BIGINT NOT NULL DEFAULT 0,
	error              TEXT
)`

const importRunCoverageTableDDL = `CREATE TABLE IF NOT EXISTS import_run_coverage (
	import_run_id BIGINT NOT NULL,
	metric_name   TEXT NOT NULL,
	source        TEXT NOT NULL DEFAULT '',
	local_date    TEXT NOT NULL,
	PRIMARY KEY (import_run_id, metric_name, source, local_date)
)`

const importStagePointsTableDDL = `CREATE TABLE IF NOT EXISTS import_stage_points (
	import_run_id BIGINT NOT NULL REFERENCES import_runs(id) ON DELETE CASCADE,
	staged_seq    BIGSERIAL PRIMARY KEY,
	metric_name   TEXT NOT NULL,
	units         TEXT,
	date          TEXT NOT NULL,
	qty           REAL,
	source        TEXT NOT NULL DEFAULT '',
	local_date    TEXT NOT NULL
)`

const importStageWorkoutsTableDDL = `CREATE TABLE IF NOT EXISTS import_stage_workouts (
	import_run_id      BIGINT NOT NULL REFERENCES import_runs(id) ON DELETE CASCADE,
	staged_seq         BIGSERIAL PRIMARY KEY,
	external_id        TEXT NOT NULL,
	name               TEXT NOT NULL,
	start_time         TIMESTAMPTZ NOT NULL,
	end_time           TIMESTAMPTZ NOT NULL,
	duration_sec       DOUBLE PRECISION NOT NULL,
	is_indoor          BOOLEAN NOT NULL DEFAULT FALSE,
	location           TEXT,
	avg_hr_bpm         DOUBLE PRECISION,
	max_hr_bpm         DOUBLE PRECISION,
	energy_kcal        DOUBLE PRECISION,
	intensity          DOUBLE PRECISION,
	distance_km        DOUBLE PRECISION,
	avg_speed_kmh      DOUBLE PRECISION,
	max_speed_kmh      DOUBLE PRECISION,
	elevation_up_m     DOUBLE PRECISION,
	step_count_total   INTEGER,
	step_cadence_spm   DOUBLE PRECISION,
	temperature_c      DOUBLE PRECISION,
	humidity_pct       DOUBLE PRECISION,
	hr_z1_sec          INTEGER,
	hr_z2_sec          INTEGER,
	hr_z3_sec          INTEGER,
	hr_z4_sec          INTEGER,
	hr_z5_sec          INTEGER
)`

func importStageIndexMigrations() []indexMigration {
	return []indexMigration{
		{"idx_import_stage_points_dedup", `CREATE INDEX IF NOT EXISTS idx_import_stage_points_dedup ON import_stage_points (import_run_id, metric_name, date, source, staged_seq DESC)`},
		{"idx_import_stage_points_coverage", `CREATE INDEX IF NOT EXISTS idx_import_stage_points_coverage ON import_stage_points (import_run_id, metric_name, source, local_date)`},
		{"idx_import_stage_workouts_dedup", `CREATE INDEX IF NOT EXISTS idx_import_stage_workouts_dedup ON import_stage_workouts (import_run_id, external_id, staged_seq DESC)`},
		{"idx_import_stage_workouts_synthetic", `CREATE INDEX IF NOT EXISTS idx_import_stage_workouts_synthetic ON import_stage_workouts (import_run_id, name, start_time, end_time)`},
	}
}
