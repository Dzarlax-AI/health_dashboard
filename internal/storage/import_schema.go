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
