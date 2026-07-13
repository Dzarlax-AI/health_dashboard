package storage

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

type columnMigration struct {
	table  string
	column string
	ddl    string
}

type indexMigration struct {
	name string
	ddl  string
}

type columnRef struct {
	table  string
	column string
}

const (
	ddlColumnStatementTimeout = 15 * time.Second
	ddlIndexStatementTimeout  = 5 * time.Minute
	ddlLockTimeout            = "5s"
)

// EnsureIndexes creates expression indexes that speed up common queries and
// applies schema migrations that aren't part of init.sql. Safe to call on
// every startup — uses IF NOT EXISTS.
func (s *DB) EnsureIndexes() {
	tableMigrations := []string{
		importRunsTableDDL,
		importRunCoverageTableDDL,
		importStagePointsTableDDL,
		importStageWorkoutsTableDDL,
		notificationDeliveriesTableDDL,
	}
	for _, ddl := range tableMigrations {
		if err := s.execStartupDDL(ddl, ddlColumnStatementTimeout); err != nil {
			log.Printf("migrate table: %v (query: %.80s)", err, ddl)
		}
	}

	// Schema migrations. Kept here (not in init.sql) so existing deployments
	// pick them up without manual intervention. ADD COLUMN IF NOT EXISTS is a
	// metadata-only change in Postgres ≥ 11 — fast even on the 3.7M-row table.
	migrations := []columnMigration{
		{"import_runs", "heartbeat_at", `ALTER TABLE import_runs ADD COLUMN IF NOT EXISTS heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW()`},
		{"import_runs", "lease_token", `ALTER TABLE import_runs ADD COLUMN IF NOT EXISTS lease_token UUID`},
		{"health_records", "processing_status", `ALTER TABLE health_records ADD COLUMN IF NOT EXISTS processing_status TEXT NOT NULL DEFAULT 'complete'`},
		{"health_records", "processing_kind", `ALTER TABLE health_records ADD COLUMN IF NOT EXISTS processing_kind TEXT NOT NULL DEFAULT 'all'`},
		{"health_records", "processing_error", `ALTER TABLE health_records ADD COLUMN IF NOT EXISTS processing_error TEXT`},
		{"health_records", "processed_at", `ALTER TABLE health_records ADD COLUMN IF NOT EXISTS processed_at TIMESTAMPTZ`},
		// quality flag for soft-suspect / hard-impossible filtering. Default
		// 'ok' so existing rows behave identically until something flips them.
		{"metric_points", "quality", `ALTER TABLE metric_points ADD COLUMN IF NOT EXISTS quality TEXT NOT NULL DEFAULT 'ok'`},
		{"metric_points", "origin", `ALTER TABLE metric_points ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT 'live'`},
		{"metric_points", "import_run_id", `ALTER TABLE metric_points ADD COLUMN IF NOT EXISTS import_run_id BIGINT`},
		// Nullable is intentional: legacy rows fall back to received_at. A
		// volatile NOW() default would rewrite multi-million-row tables during
		// upgrade; every current writer sets the value explicitly instead.
		{"metric_points", "source_snapshot_at", `ALTER TABLE metric_points ADD COLUMN IF NOT EXISTS source_snapshot_at TIMESTAMPTZ`},
		{"workouts", "origin", `ALTER TABLE workouts ADD COLUMN IF NOT EXISTS origin TEXT NOT NULL DEFAULT 'live'`},
		{"workouts", "import_run_id", `ALTER TABLE workouts ADD COLUMN IF NOT EXISTS import_run_id BIGINT`},
		{"workouts", "source_snapshot_at", `ALTER TABLE workouts ADD COLUMN IF NOT EXISTS source_snapshot_at TIMESTAMPTZ`},
		// EnergyBank EOD snapshot columns. Capacity / current / drain are 0–100;
		// verdict is "rest" / "active_recovery" / "moderate" / "push_hard" (matches
		// EnergyBank.ActionVerdict). NULL means no snapshot was taken — the row
		// pre-dates the persistence wiring or briefing didn't run that day.
		{"daily_scores", "energy_capacity", `ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS energy_capacity INTEGER`},
		{"daily_scores", "energy_eod_current", `ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS energy_eod_current INTEGER`},
		{"daily_scores", "energy_drain", `ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS energy_drain INTEGER`},
		{"daily_scores", "energy_verdict", `ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS energy_verdict TEXT`},

		// v2.2 stress methodology — median HR over the last 3h of the
		// main sleep segment ending on this date. NOT to be confused
		// with `rhr_avg` (which is a per-day average of heart_rate —
		// see STRESS_MEASUREMENT.md §0 blocker 1). NULL until the
		// `upsertBaselineHROvernightForDate` writer runs for the date.
		{"daily_scores", "baseline_hr_overnight", `ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS baseline_hr_overnight REAL`},

		// v2.2 sustained_hr_load — §4.4 z-load integral over the awake
		// window. Per-day cache so ComputeBankForDate's 21-day SELECT
		// stays one query instead of 21·(WakeTime+Coverage+Baseline+
		// HourlySeries) round-trips. NULL when coverage gate fails
		// (<8h HR-covered awake) or baselines are still in
		// calibration; in either case DrainV2's β term contributes 0
		// for that day and falls back to v2.0 (kcal-only) drain.
		{"daily_scores", "sustained_hr_load", `ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS sustained_hr_load REAL`},

		// v2.2 stress_flags — §4.3 stratified flags computed alongside
		// sustained_hr_load. Currently surfaces the HR-z-derived
		// flags only: stale_stress (coverage gate fired) /
		// calibration_warmup / acute_stress (z>+2 hour exists) /
		// sustained_load (≥4h run at z>+1). Multi-channel flags
		// (illness_signature / recovery_debt /
		// parasympathetic_rebound) land in a follow-up PR.
		{"daily_scores", "stress_flags", `ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS stress_flags TEXT[]`},

		// sleep_unspecified — coarse asleep time from sources without a
		// deep/REM/core breakdown (RingConn, iPhone-only, older Apple
		// Watch). Splits the lie out of sleep_core, which used to absorb
		// it. NULL on rows that pre-date the iOS split; aggregator
		// treats NULL as 0 in sleep_total.
		{"daily_scores", "sleep_unspecified", `ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS sleep_unspecified REAL`},
	}

	existingColumns, err := s.existingColumns(migrations)
	if err != nil {
		log.Printf("migrate: catalog check failed: %v", err)
	} else {
		migrations = pendingColumnMigrations(migrations, existingColumns)
	}
	for _, migration := range migrations {
		if err := s.execStartupDDL(migration.ddl, ddlColumnStatementTimeout); err != nil {
			log.Printf("migrate: %v (query: %.80s)", err, migration.ddl)
		}
	}

	indexes := []indexMigration{
		// Partial index covers the hot path — baseline reads filter quality='ok'.
		// Defined inline (not in the migration block) because it depends on the
		// quality column existing first.
		{"idx_points_quality_metric", `CREATE INDEX IF NOT EXISTS idx_points_quality_metric ON metric_points (metric_name, SUBSTRING(date,1,10)) WHERE quality = 'ok'`},

		// Speeds up GROUP BY / ORDER BY on the date part of hourly_metrics.hour
		{"idx_hourly_date", `CREATE INDEX IF NOT EXISTS idx_hourly_date ON hourly_metrics (SUBSTRING(hour,1,10))`},

		// Speeds up WHERE metric_name = $1 AND date-part queries on hourly_metrics
		{"idx_hourly_metric_date", `CREATE INDEX IF NOT EXISTS idx_hourly_metric_date ON hourly_metrics (metric_name, SUBSTRING(hour,1,10))`},

		// Speeds up GROUP BY / ORDER BY on the date part of metric_points.date
		{"idx_points_date", `CREATE INDEX IF NOT EXISTS idx_points_date ON metric_points (SUBSTRING(date,1,10))`},

		// Speeds up WHERE metric_name = $1 AND date-part queries on metric_points
		{"idx_points_metric_date", `CREATE INDEX IF NOT EXISTS idx_points_metric_date ON metric_points (metric_name, SUBSTRING(date,1,10))`},
	}
	indexes = append(indexes, importStageIndexMigrations()...)

	existingIndexes, err := s.existingIndexes(indexes)
	if err != nil {
		log.Printf("ensure index: catalog check failed: %v", err)
	} else {
		indexes = pendingIndexMigrations(indexes, existingIndexes)
	}
	for _, index := range indexes {
		if err := s.execStartupDDL(index.ddl, ddlIndexStatementTimeout); err != nil {
			log.Printf("ensure index: %v (query: %.80s)", err, index.ddl)
		}
	}
	if err := s.CleanupAbandonedImportStages(24 * time.Hour); err != nil {
		log.Printf("cleanup import staging: %v", err)
	}
}

func pendingColumnMigrations(migrations []columnMigration, existing map[columnRef]bool) []columnMigration {
	pending := make([]columnMigration, 0, len(migrations))
	for _, migration := range migrations {
		if !existing[columnRef{table: migration.table, column: migration.column}] {
			pending = append(pending, migration)
		}
	}
	return pending
}

func pendingIndexMigrations(indexes []indexMigration, existing map[string]bool) []indexMigration {
	pending := make([]indexMigration, 0, len(indexes))
	for _, index := range indexes {
		if !existing[index.name] {
			pending = append(pending, index)
		}
	}
	return pending
}

func (s *DB) existingColumns(migrations []columnMigration) (map[columnRef]bool, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	wanted := make(map[columnRef]bool, len(migrations))
	for _, migration := range migrations {
		wanted[columnRef{table: migration.table, column: migration.column}] = false
	}

	// New and NewWithSchema configure the pool for pgx SimpleProtocol, so
	// these startup catalog queries do not add prepared-statement churn.
	rows, err := s.pool.Query(ctx, `
		SELECT table_name, column_name
		  FROM information_schema.columns
		 WHERE table_schema = current_schema()
	`, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var ref columnRef
		if err := rows.Scan(&ref.table, &ref.column); err != nil {
			return nil, err
		}
		if _, ok := wanted[ref]; ok {
			wanted[ref] = true
		}
	}
	return wanted, rows.Err()
}

func (s *DB) existingIndexes(indexes []indexMigration) (map[string]bool, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	wanted := make(map[string]bool, len(indexes))
	for _, index := range indexes {
		wanted[index.name] = false
	}

	rows, err := s.pool.Query(ctx, `
		SELECT indexname
		  FROM pg_indexes
		 WHERE schemaname = current_schema()
	`, pgx.QueryExecModeSimpleProtocol)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if _, ok := wanted[name]; ok {
			wanted[name] = true
		}
	}
	return wanted, rows.Err()
}

func (s *DB) execStartupDDL(ddl string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// The pool uses pgx SimpleProtocol by default; keep startup DDL out of
	// prepared-statement caching while we briefly take schema/index locks.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())

	if _, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, pgx.QueryExecModeSimpleProtocol, ddlLockTimeout); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, ddl, pgx.QueryExecModeSimpleProtocol); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
