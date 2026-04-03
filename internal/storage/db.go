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
	ctx, cancel := queryCtx()
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)

	var recordID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO health_records
		(automation_name, automation_id, automation_aggregation, automation_period, session_id, content_type, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id`,
		r.AutomationName, r.AutomationID, r.AutomationAggregation,
		r.AutomationPeriod, r.SessionID, r.ContentType, r.Payload,
	).Scan(&recordID)
	if err != nil {
		return 0, err
	}

	// For sleep midnight summaries: allow upward corrections up to +30%,
	// but block larger jumps that indicate Health Auto Export accumulation bug.
	// This lets partial→full sync through (e.g. 6.3→7.3 = +16%)
	// while blocking accumulation (e.g. 7.3→10.8 = +48%).
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

	for _, p := range points {
		if _, err := tx.Exec(ctx, upsertSQL, recordID, p.MetricName, p.Units, p.Date, p.Qty, p.Source); err != nil {
			return 0, fmt.Errorf("insert point %s/%s: %w", p.MetricName, p.Date, err)
		}
	}

	return recordID, tx.Commit(ctx)
}
