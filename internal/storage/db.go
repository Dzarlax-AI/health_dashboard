package storage

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type DB struct {
	db                 *sql.DB
	needsForceBackfill bool // set by migrate() when data-altering migrations ran
}

// NeedsForceBackfill returns true if startup migrations changed metric_points
// data (renames, unit conversions) and the caches need a full rebuild.
func (s *DB) NeedsForceBackfill() bool { return s.needsForceBackfill }

func New(path string) (*DB, error) {
	db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping db: %w", err)
	}
	s := &DB{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *DB) migrate() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS health_records (
		id                     INTEGER PRIMARY KEY AUTOINCREMENT,
		received_at            DATETIME DEFAULT CURRENT_TIMESTAMP,
		automation_name        TEXT,
		automation_id          TEXT,
		automation_aggregation TEXT,
		automation_period      TEXT,
		session_id             TEXT,
		content_type           TEXT,
		payload                TEXT
	)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS metric_points (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		health_record_id INTEGER NOT NULL REFERENCES health_records(id),
		received_at      DATETIME DEFAULT CURRENT_TIMESTAMP,
		metric_name      TEXT NOT NULL,
		units            TEXT,
		date             TEXT NOT NULL,
		qty              REAL,
		source           TEXT,
		UNIQUE(metric_name, date, source)
	)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE INDEX IF NOT EXISTS idx_metric_points_name_date ON metric_points(metric_name, date)`)
	if err != nil {
		return err
	}
	if err := s.migrateMetricNames(); err != nil {
		return err
	}
	if err := s.migrateFractionToPercent(); err != nil {
		return err
	}
	if err := s.migrateDailyScores(); err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS minute_metrics (
		metric_name TEXT NOT NULL,
		minute      TEXT NOT NULL,
		source      TEXT NOT NULL DEFAULT '',
		avg_val     REAL NOT NULL,
		min_val     REAL NOT NULL,
		max_val     REAL NOT NULL,
		PRIMARY KEY (metric_name, minute, source)
	)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS hourly_metrics (
		metric_name TEXT NOT NULL,
		hour        TEXT NOT NULL,
		source      TEXT NOT NULL DEFAULT '',
		avg_val     REAL NOT NULL,
		min_val     REAL NOT NULL,
		max_val     REAL NOT NULL,
		PRIMARY KEY (metric_name, hour, source)
	)`)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`CREATE TABLE IF NOT EXISTS settings (
		key        TEXT NOT NULL PRIMARY KEY,
		value      TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL DEFAULT (datetime('now'))
	)`)
	return err
}

// migrateMetricNames renames legacy metric names to canonical ones.
// Idempotent: only affects rows with old names. If a rename would create a
// duplicate (same metric_name+date+source already exists), the old row is
// deleted instead of renamed.
func (s *DB) migrateMetricNames() error {
	renames := [][2]string{
		{"heart_rate_variability_sdnn", "heart_rate_variability"},
		{"oxygen_saturation", "blood_oxygen_saturation"},
	}
	for _, r := range renames {
		old, canonical := r[0], r[1]
		// Check if any rows with the old name exist (fast path: skip if nothing to do).
		var cnt int
		s.db.QueryRow(`SELECT COUNT(*) FROM metric_points WHERE metric_name = ?`, old).Scan(&cnt)
		if cnt == 0 {
			continue
		}
		// Delete old-name rows that would conflict with existing canonical-name rows.
		if _, err := s.db.Exec(`
			DELETE FROM metric_points
			WHERE metric_name = ?
			  AND (date, source) IN (
				SELECT date, source FROM metric_points WHERE metric_name = ?
			  )`, old, canonical); err != nil {
			return fmt.Errorf("dedup metric rename %s→%s: %w", old, canonical, err)
		}
		// Rename remaining old-name rows.
		if _, err := s.db.Exec(
			`UPDATE metric_points SET metric_name = ? WHERE metric_name = ?`,
			canonical, old); err != nil {
			return fmt.Errorf("rename metric %s→%s: %w", old, canonical, err)
		}
		// Also rename in cache tables.
		for _, tbl := range []string{"minute_metrics", "hourly_metrics"} {
			s.db.Exec(`DELETE FROM `+tbl+` WHERE metric_name = ?`, old)
		}
		s.needsForceBackfill = true
		log.Printf("migrated metric_points: %s → %s (%d rows)", old, canonical, cnt)
	}
	return nil
}

// migrateFractionToPercent fixes percentage metrics that were imported from
// Apple Health XML as fractions (0.0–1.0) instead of percentages (0–100).
// Idempotent: only affects rows where qty ≤ 1.0 for metrics that should be 0–100.
// Also invalidates cache tables for affected metrics so they get recomputed.
func (s *DB) migrateFractionToPercent() error {
	metrics := []string{
		"blood_oxygen_saturation",
		"body_fat_percentage",
		"walking_asymmetry",
		"walking_double_support",
		"walking_steadiness",
	}
	for _, m := range metrics {
		var cnt int
		s.db.QueryRow(
			`SELECT COUNT(*) FROM metric_points WHERE metric_name = ? AND qty > 0 AND qty <= 1.0`, m,
		).Scan(&cnt)
		if cnt == 0 {
			continue
		}
		if _, err := s.db.Exec(
			`UPDATE metric_points SET qty = qty * 100 WHERE metric_name = ? AND qty > 0 AND qty <= 1.0`, m,
		); err != nil {
			return fmt.Errorf("migrate fraction→percent %s: %w", m, err)
		}
		// Invalidate caches for this metric.
		for _, tbl := range []string{"minute_metrics", "hourly_metrics"} {
			s.db.Exec(`DELETE FROM `+tbl+` WHERE metric_name = ?`, m)
		}
		s.needsForceBackfill = true
		log.Printf("migrated %s: %d rows fraction→percent (×100)", m, cnt)
	}
	return nil
}

// migrateDailyScores creates or upgrades the daily_scores table.
// v1 had NOT NULL on readiness; v2 adds 13 metric columns and makes all
// computed columns nullable so metric and readiness fills can happen
// independently. Safe to call on every startup.
func (s *DB) migrateDailyScores() error {
	// Check whether the table needs creating or upgrading.
	var hasMetrics int
	s.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('daily_scores') WHERE name='hrv_avg'`).Scan(&hasMetrics)

	if hasMetrics == 0 {
		// Create new table or upgrade existing one via rename-copy-drop.
		s.db.Exec(`CREATE TABLE IF NOT EXISTS daily_scores_new (
			date          TEXT NOT NULL PRIMARY KEY,
			readiness     INTEGER,
			score_version INTEGER,
			computed_at   TEXT NOT NULL DEFAULT (datetime('now')),
			hrv_avg       REAL,
			rhr_avg       REAL,
			sleep_total   REAL,
			sleep_deep    REAL,
			sleep_rem     REAL,
			sleep_core    REAL,
			sleep_awake   REAL,
			steps         REAL,
			calories      REAL,
			exercise_min  REAL,
			spo2_avg      REAL,
			vo2_avg       REAL,
			resp_avg      REAL
		)`)
		// Copy any existing rows (readiness scores survive the upgrade).
		s.db.Exec(`INSERT OR IGNORE INTO daily_scores_new (date, readiness, score_version, computed_at)
			SELECT date, readiness, score_version, computed_at FROM daily_scores`)
		s.db.Exec(`DROP TABLE IF EXISTS daily_scores`)
		if _, err := s.db.Exec(`ALTER TABLE daily_scores_new RENAME TO daily_scores`); err != nil {
			return fmt.Errorf("migrate daily_scores: %w", err)
		}
	}
	return nil
}

func (s *DB) Close() error {
	return s.db.Close()
}

// parseDate parses a YYYY-MM-DD string.
func parseDate(s string) (time.Time, error) {
	return time.Parse("2006-01-02", s)
}

// Record is the raw payload received from Health Auto Export.
type Record struct {
	AutomationName        string
	AutomationID          string
	AutomationAggregation string
	AutomationPeriod      string
	SessionID             string
	ContentType           string
	Payload               string
}

// MetricPoint is a single parsed data point stored in metric_points.
type MetricPoint struct {
	MetricName string
	Units      string
	Date       string
	Qty        float64
	Source     string
}

func (s *DB) Insert(r Record, points []MetricPoint) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO health_records
		(automation_name, automation_id, automation_aggregation, automation_period, session_id, content_type, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.AutomationName, r.AutomationID, r.AutomationAggregation,
		r.AutomationPeriod, r.SessionID, r.ContentType, r.Payload,
	)
	if err != nil {
		return 0, err
	}
	recordID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO metric_points
		(health_record_id, metric_name, units, date, qty, source)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	for _, p := range points {
		if _, err := stmt.Exec(recordID, p.MetricName, p.Units, p.Date, p.Qty, p.Source); err != nil {
			return 0, fmt.Errorf("insert point %s/%s: %w", p.MetricName, p.Date, err)
		}
	}

	return recordID, tx.Commit()
}
