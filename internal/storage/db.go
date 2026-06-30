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

	// aiRegenInFlight dedupes concurrent EnsureTodayAIInsight calls so
	// concurrent pollers (and overlapping sync callers — morning smart-retry,
	// test-notify, opportunistic ingest trigger) don't multiply Gemini calls.
	// Keyed by "<date>|<lang>". The LoadOrStore happens inside
	// EnsureTodayAIInsight using its own derived `today`, so the key always
	// matches the date being generated (no midnight-rollover race).
	aiRegenInFlight sync.Map

	// aiRegenLastFailAt records the wall-clock time of the last failed
	// EnsureTodayAIInsight (zero blocks saved). Keyed by "<date>|<lang>".
	// When set, the next ~5 min of regen attempts return the (still empty)
	// cache instead of hitting Gemini again — avoids per-minute pollers
	// hammering the API during a sustained Gemini outage.
	aiRegenLastFailAt sync.Map
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
	// Pool budget per tenant — see "Pool budget" note below. With 2 tenants
	// this caps us at 2 × 8 = 16 connections from this service even under
	// peak (force backfill + busy ingest), leaving headroom for authentik
	// and the rest of the stack on a shared 50-conn Postgres.
	config.MaxConns = 8
	config.MinConns = 2
	config.MaxConnIdleTime = 5 * time.Minute
	config.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	// Quote the schema identifier via pgx.Identifier rather than raw
	// concatenation. Production callers pass `health` / `health_mariia`
	// where this doesn't matter, but the same code path also runs from
	// test setup with synthesised names — keep the quoting consistent
	// with CreateSchema / DropSchema so the rule is "all DDL with a
	// dynamic name goes through Identifier.Sanitize()".
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	config.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path = "+quotedSchema)
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
			date              TEXT PRIMARY KEY,
			readiness         INTEGER,
			score_version     INTEGER,
			computed_at       TEXT NOT NULL DEFAULT NOW()::text,
			hrv_avg           REAL,
			rhr_avg           REAL,
			sleep_total       REAL,
			sleep_deep        REAL,
			sleep_rem         REAL,
			sleep_core        REAL,
			sleep_unspecified REAL,
			sleep_awake       REAL,
			steps             REAL,
			calories          REAL,
			exercise_min      REAL,
			spo2_avg          REAL,
			vo2_avg           REAL,
			resp_avg          REAL,
			energy_capacity     INTEGER,
			energy_eod_current  INTEGER,
			energy_drain        INTEGER,
			energy_verdict      TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS settings (
			key        TEXT PRIMARY KEY,
			value      TEXT NOT NULL DEFAULT '',
			updated_at TEXT NOT NULL DEFAULT NOW()::text
		)`,
		// One row per Apple Health workout. We store summary fields only —
		// timeseries (route, per-minute HR, per-second energy) are large
		// (3000+ points for an outdoor run) and are not used by any text
		// analysis path, so they are dropped on ingest. Time-in-HR-zone is
		// pre-computed on ingest from heartRateData[] when zones are
		// configured (HEALTH_HR_ZONES_BPM env var) and stored as five
		// integer columns.
		`CREATE TABLE IF NOT EXISTS workouts (
			id                BIGSERIAL PRIMARY KEY,
			received_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			health_record_id  BIGINT REFERENCES health_records(id),
			external_id       TEXT UNIQUE NOT NULL,
			name              TEXT NOT NULL,
			start_time        TIMESTAMPTZ NOT NULL,
			end_time          TIMESTAMPTZ NOT NULL,
			duration_sec      DOUBLE PRECISION NOT NULL,
			is_indoor         BOOLEAN NOT NULL DEFAULT FALSE,
			location          TEXT,
			avg_hr_bpm        DOUBLE PRECISION,
			max_hr_bpm        DOUBLE PRECISION,
			energy_kcal       DOUBLE PRECISION,
			intensity         DOUBLE PRECISION,
			distance_km       DOUBLE PRECISION,
			avg_speed_kmh     DOUBLE PRECISION,
			max_speed_kmh     DOUBLE PRECISION,
			elevation_up_m    DOUBLE PRECISION,
			step_count_total  INTEGER,
			step_cadence_spm  DOUBLE PRECISION,
			temperature_c     DOUBLE PRECISION,
			humidity_pct      DOUBLE PRECISION,
			hr_z1_sec         INTEGER,
			hr_z2_sec         INTEGER,
			hr_z3_sec         INTEGER,
			hr_z4_sec         INTEGER,
			hr_z5_sec         INTEGER,
			CONSTRAINT chk_workout_times CHECK (end_time >= start_time)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_workouts_start_time ON workouts (start_time DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_workouts_name ON workouts (name)`,
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
	// Pool budget — see NewWithSchema for the rationale. Single-tenant
	// gets a slightly larger budget (no second pool eating into the
	// shared 50-conn Postgres ceiling).
	config.MaxConns = 12
	config.MinConns = 2
	config.MaxConnIdleTime = 5 * time.Minute
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

// NewFromPool wraps an existing pgx pool in a DB handle. The caller
// transfers ownership of the pool; DB.Close will close it.
func NewFromPool(pool *pgxpool.Pool) *DB {
	return &DB{pool: pool}
}

// CreateSchema issues CREATE SCHEMA for the given name.
// Returns an error (which may be *registry-compatible ErrNeedsManualSetup via the caller)
// if the DB user lacks the necessary privileges.
//
// `name` is run through pgx.Identifier.Sanitize() so an attacker-controlled
// identifier (or a malformed one from a misconfigured caller) cannot
// inject additional DDL. Production tenants use plain ASCII names
// (`health`, `health_mariia`) where quoting is a no-op semantically, so
// this is a defence-in-depth change, not a behaviour change.
func (s *DB) CreateSchema(ctx context.Context, name string) error {
	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{name}.Sanitize())
	return err
}

// DropSchema issues DROP SCHEMA … CASCADE. Exposed for test teardown
// where the pool used to create the schema is not the same pool that
// needs to drop it (the schema-pinned pool can't reach across packages).
// Production code does not call this; tenant teardown goes through
// registry.RemoveUser. Same Identifier.Sanitize() guard as CreateSchema.
func (s *DB) DropSchema(ctx context.Context, name string) error {
	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{name}.Sanitize()+" CASCADE")
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

	// Three guards on the sleep_% UPDATE branch — all protect the cached
	// per-night aggregate from being clobbered by garbage records that share
	// the same (metric_name, date, source) key:
	//
	//   1. RingConn midnight-summary inflation (recent nights only):
	//      ignore a new value that is more than 30% larger than the existing
	//      one when both are non-trivial AND the existing row is from
	//      RingConn AND the night is within the last 3 days. Catches the
	//      RingConn 14h-night outlier pattern in real time, while still
	//      allowing free upward normalisation on older "stuck" rows after
	//      iOS-side bug fixes (legitimate factor-of-7 corrections were
	//      blocked by the original 1.3× / 30-day / all-source guard — see
	//      Todoist 6gXCG8Qgpc73J4J7 for the incident log).
	//
	//   2. Zero-payload overwrite: a per-day chunked re-sync from iOS may
	//      emit `qty=0` for a source that has only `.inBed` / late-evening
	//      samples in the chunk's window (the actual sleep block started
	//      the prior evening and falls outside the chunk). The client now
	//      widens the sleep predicate, but as a belt-and-suspenders we keep
	//      the existing positive value if the incoming one is zero. Applies
	//      to all sleep_* metrics (zero is never a legitimate update).
	//
	//   3. sleep_total deflation (≥50% drop) on an established record:
	//      symmetric to (1) but scoped to integral sleep_total only.
	//      Per-stage drops (sleep_deep / sleep_rem) of >50% can be a
	//      legitimate "bad night" reading — blocking those would lock in
	//      stale stage breakdowns. Concrete miss observed during a 30-day
	//      chunked re-sync — chunk_K's 12h-overlap window saw the FULL ~8h
	//      night for wake-up date K, then chunk_K+1's window saw only a 1h
	//      afternoon nap whose wake-up also landed on date K, and the
	//      latter UPSERT replaced 8.14h with 1.08h. Real total swings
	//      exceed 50% vanishingly rarely — when they do, the next sync run
	//      converges naturally.
	const upsertSQL = `INSERT INTO metric_points
		(health_record_id, metric_name, units, date, qty, source)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT(metric_name, date, source) DO UPDATE SET
			qty = CASE
				WHEN metric_points.metric_name LIKE 'sleep_%'
				  AND SUBSTRING(metric_points.date, 12, 8) = '00:00:00'
				  AND metric_points.source LIKE '%RingConn%'
				  AND SUBSTRING(metric_points.date, 1, 10) >= TO_CHAR(NOW() - INTERVAL '3 days', 'YYYY-MM-DD')
				  AND metric_points.qty > 1.0
				  AND excluded.qty > metric_points.qty * 1.3
				THEN metric_points.qty
				WHEN metric_points.metric_name LIKE 'sleep_%'
				  AND metric_points.qty > 0
				  AND excluded.qty = 0
				THEN metric_points.qty
				WHEN metric_points.metric_name = 'sleep_total'
				  AND metric_points.qty > 1.0
				  AND excluded.qty > 0
				  AND excluded.qty < metric_points.qty * 0.5
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
