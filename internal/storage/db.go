package storage

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type DB struct {
	pool    *pgxpool.Pool
	cacheMu sync.Mutex // protects concurrent writes to hourly_metrics and daily_scores
}

// queryCtx returns a context with a 30-second timeout for regular queries.
func queryCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 30*time.Second)
}

// longCtx returns a context with a 5-minute timeout for heavy operations (backfill, aggregation).
func longCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 5*time.Minute)
}

// NeedsForceBackfill returns true when hourly_metrics is empty, meaning
// caches need a full rebuild.
func (s *DB) NeedsForceBackfill() bool {
	ctx, cancel := queryCtx()
	defer cancel()
	var cnt int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM hourly_metrics LIMIT 1`).Scan(&cnt); err != nil {
		log.Printf("NeedsForceBackfill: %v", err)
		return true
	}
	return cnt == 0
}

// NewWithSchema creates a DB pool that sets search_path to schema on every connection.
// This makes all unqualified table references resolve to the given schema,
// allowing the same SQL queries to serve different tenants transparently.
func NewWithSchema(ctx context.Context, connString, schema string) (*DB, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse pg config: %w", err)
	}
	config.MaxConns = 20
	config.MinConns = 5
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path = "+schema)
		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect to pg: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping pg: %w", err)
	}
	return &DB{pool: pool}, nil
}

// EnsureAllTables creates all health schema tables if they do not exist.
// Called when provisioning a new tenant schema.
func (s *DB) EnsureAllTables() error {
	ctx, cancel := longCtx()
	defer cancel()

	stmts := []string{
		`CREATE TABLE IF NOT EXISTS health_records (
			id                     BIGSERIAL PRIMARY KEY,
			received_at            TIMESTAMPTZ DEFAULT NOW(),
			automation_name        TEXT,
			automation_id          TEXT,
			automation_aggregation TEXT,
			automation_period      TEXT,
			session_id             TEXT,
			content_type           TEXT,
			payload                TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS metric_points (
			id               BIGSERIAL PRIMARY KEY,
			health_record_id BIGINT NOT NULL REFERENCES health_records(id),
			received_at      TIMESTAMPTZ DEFAULT NOW(),
			metric_name      TEXT NOT NULL,
			units            TEXT,
			date             TEXT NOT NULL,
			qty              REAL,
			source           TEXT,
			UNIQUE(metric_name, date, source)
		)`,
		`CREATE TABLE IF NOT EXISTS minute_metrics (
			metric_name TEXT NOT NULL,
			minute      TEXT NOT NULL,
			source      TEXT NOT NULL DEFAULT '',
			avg_val     REAL NOT NULL DEFAULT 0,
			min_val     REAL NOT NULL DEFAULT 0,
			max_val     REAL NOT NULL DEFAULT 0,
			PRIMARY KEY (metric_name, minute, source)
		)`,
		`CREATE TABLE IF NOT EXISTS hourly_metrics (
			metric_name TEXT NOT NULL,
			hour        TEXT NOT NULL,
			source      TEXT NOT NULL DEFAULT '',
			avg_val     REAL NOT NULL DEFAULT 0,
			min_val     REAL NOT NULL DEFAULT 0,
			max_val     REAL NOT NULL DEFAULT 0,
			PRIMARY KEY (metric_name, hour, source)
		)`,
		`CREATE TABLE IF NOT EXISTS daily_scores (
			date          TEXT PRIMARY KEY,
			readiness     INTEGER,
			score_version INTEGER,
			computed_at   TEXT NOT NULL DEFAULT NOW()::text,
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
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT NOW()::text
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("ensure table: %w", err)
		}
	}
	return nil
}

func New(ctx context.Context, connString string) (*DB, error) {
	config, err := pgxpool.ParseConfig(connString)
	if err != nil {
		return nil, fmt.Errorf("parse pg config: %w", err)
	}
	config.MaxConns = 20
	config.MinConns = 5
	// Disable automatic prepared statement caching — it causes lock contention
	// when multiple goroutines prepare the same statement concurrently.
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("connect to pg: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping pg: %w", err)
	}
	return &DB{pool: pool}, nil
}

func (s *DB) Close() {
	s.pool.Close()
}

// CreateSchema issues CREATE SCHEMA for the given name.
// Returns an error (which may be *registry-compatible ErrNeedsManualSetup via the caller)
// if the DB user lacks the necessary privileges.
func (s *DB) CreateSchema(ctx context.Context, name string) error {
	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, "CREATE SCHEMA "+name)
	return err
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

// InsertRaw saves the raw payload to health_records and returns the new record ID.
// Call InsertPoints in a goroutine afterward to parse and store metric_points.
func (s *DB) InsertRaw(r Record) (int64, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var recordID int64
	err := s.pool.QueryRow(ctx,
		`INSERT INTO health_records
		(automation_name, automation_id, automation_aggregation, automation_period, session_id, content_type, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		r.AutomationName, r.AutomationID, r.AutomationAggregation,
		r.AutomationPeriod, r.SessionID, r.ContentType, r.Payload,
	).Scan(&recordID)
	return recordID, err
}

// InsertPoints upserts parsed metric_points for a previously saved health_record.
// For sleep midnight summaries: allow upward corrections up to +30%,
// but block larger jumps that indicate Health Auto Export accumulation bug.
func (s *DB) InsertPoints(recordID int64, points []MetricPoint) error {
	if len(points) == 0 {
		return nil
	}
	ctx, cancel := longCtx()
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	const upsertSQL = `INSERT INTO metric_points
		(health_record_id, metric_name, units, date, qty, source)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT(metric_name, date, source) DO UPDATE SET
			qty = CASE
				WHEN metric_points.metric_name LIKE 'sleep_%'
				  AND SUBSTRING(metric_points.date, 12, 8) = '00:00:00'
				  AND metric_points.qty > 1.0
				  AND excluded.qty > metric_points.qty * 1.3
				THEN metric_points.qty
				ELSE excluded.qty
			END,
			units = excluded.units,
			health_record_id = excluded.health_record_id`

	const chunkSize = 500
	for i := 0; i < len(points); i += chunkSize {
		end := i + chunkSize
		if end > len(points) {
			end = len(points)
		}
		chunk := points[i:end]
		batch := &pgx.Batch{}
		for _, p := range chunk {
			batch.Queue(upsertSQL, recordID, p.MetricName, p.Units, p.Date, p.Qty, p.Source)
		}
		br := tx.SendBatch(ctx, batch)
		for _, p := range chunk {
			if _, err := br.Exec(); err != nil {
				br.Close()
				return fmt.Errorf("insert point %s/%s: %w", p.MetricName, p.Date, err)
			}
		}
		if err := br.Close(); err != nil {
			return fmt.Errorf("batch close: %w", err)
		}
	}

	return tx.Commit(ctx)
}
