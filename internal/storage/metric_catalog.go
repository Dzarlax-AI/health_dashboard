package storage

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	metricCatalogKey       = "_metric_catalog_snapshot"
	metricCatalogSourceKey = "_metric_catalog_source_generation"
	metricCatalogHoursKey  = "_metric_catalog_hours_generation"
	metricCatalogFormat    = 1
	// Shared by tenant roles and by the preparation command within this database.
	metricCatalogBuildLock int64 = 0x484B434154414C47
)

var (
	ErrMetricCatalogUnavailable = errors.New("metric catalog is being prepared")
	ErrMetricCatalogBusy        = errors.New("metric catalog builder is busy")
	// Bump the format when changing the Go-side selection or persisted encoding.
	metricCatalogSemantics = fmt.Sprintf("%x", sha256.Sum256([]byte(
		hourlyCatalogSQL+compatibilityCatalogSQL()+fmt.Sprint(compatibilityStoredMetricNames()),
	)))
)

type metricCatalogSnapshot struct {
	Format     int             `json:"format"`
	Semantics  string          `json:"semantics"`
	BuiltAt    time.Time       `json:"built_at"`
	CapturedAt time.Time       `json:"captured_at"`
	Generation string          `json:"generation"`
	Metrics    []MetricSummary `json:"metrics"`
}

// ReadMetricCatalog reads one setting via the authenticated tenant's pool.
func (s *DB) ReadMetricCatalog(ctx context.Context, legacy bool) ([]MetricSummary, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	snapshot, err := s.readMetricCatalog(ctx)
	if err != nil {
		return nil, err
	}
	return metricCatalogForContract(snapshot.Metrics, legacy), nil
}

func (s *DB) readMetricCatalog(ctx context.Context) (metricCatalogSnapshot, error) {
	var raw string
	err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1`, metricCatalogKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return metricCatalogSnapshot{}, ErrMetricCatalogUnavailable
	}
	if err != nil {
		return metricCatalogSnapshot{}, fmt.Errorf("read metric catalog: %w", err)
	}
	var snapshot metricCatalogSnapshot
	if json.Unmarshal([]byte(raw), &snapshot) != nil ||
		snapshot.Format != metricCatalogFormat || snapshot.Semantics != metricCatalogSemantics ||
		snapshot.CapturedAt.IsZero() || snapshot.BuiltAt.Before(snapshot.CapturedAt) || snapshot.Generation == "" || snapshot.Metrics == nil {
		return metricCatalogSnapshot{}, ErrMetricCatalogUnavailable
	}
	seen := make(map[string]bool, len(snapshot.Metrics))
	for _, m := range snapshot.Metrics {
		if m.Count <= 0 || m.Min > m.Max || seen[m.Name] {
			return metricCatalogSnapshot{}, ErrMetricCatalogUnavailable
		}
		seen[m.Name] = true
	}
	return snapshot, nil
}

type catalogTransaction interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

// MarkMetricCatalogSourceChangedTx tracks point mutations in their transaction.
func MarkMetricCatalogSourceChangedTx(ctx context.Context, tx catalogTransaction) error {
	return markMetricCatalogGeneration(ctx, tx, metricCatalogSourceKey)
}

// MarkMetricCatalogHoursChangedTx tracks aggregate mutations in their transaction.
func MarkMetricCatalogHoursChangedTx(ctx context.Context, tx catalogTransaction) error {
	return markMetricCatalogGeneration(ctx, tx, metricCatalogHoursKey)
}

func markMetricCatalogGeneration(ctx context.Context, tx catalogTransaction, key string) error {
	// Separate rows avoid acquiring a common generation row both before
	// source writes and after aggregate writes. The builder reads both
	// under the same snapshot as the tables they track.
	_, err := tx.Exec(ctx, `
		INSERT INTO settings (key,value,updated_at)
		VALUES ($1,pg_current_xact_id()::text,clock_timestamp()::text)
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=EXCLUDED.updated_at
		WHERE settings.value IS DISTINCT FROM EXCLUDED.value`, key)
	return err
}

func (s *DB) metricCatalogGeneration(ctx context.Context) (string, error) {
	var generation string
	err := s.pool.QueryRow(ctx, `SELECT
		COALESCE((SELECT value FROM settings WHERE key=$1), '0') || '/' ||
		COALESCE((SELECT value FROM settings WHERE key=$2), '0')`,
		metricCatalogSourceKey, metricCatalogHoursKey).Scan(&generation)
	return generation, err
}

func newMetricCatalog(capturedAt time.Time, generation string, rows []MetricSummary) metricCatalogSnapshot {
	if rows == nil {
		rows = []MetricSummary{}
	}
	return metricCatalogSnapshot{
		Format: metricCatalogFormat, Semantics: metricCatalogSemantics,
		CapturedAt: capturedAt, BuiltAt: time.Now().UTC(), Generation: generation, Metrics: rows,
	}
}

// RefreshMetricCatalog builds the exact snapshot only when needed. A nonzero
// reconcileAfter forces one reconciliation after startup, including after a
// rollback to a binary whose writers did not track catalog changes.
func (s *DB) RefreshMetricCatalog(ctx context.Context, reconcileAfter time.Time, minInterval time.Duration) (bool, error) {
	return s.refreshMetricCatalog(ctx, reconcileAfter, minInterval, false)
}

// PrepareMetricCatalog writes one snapshot after comparing it with the old
// full-ranking query on the same database state.
func (s *DB) PrepareMetricCatalog(ctx context.Context) error {
	_, err := s.refreshMetricCatalog(ctx, time.Time{}, 0, true)
	return err
}

func (s *DB) refreshMetricCatalog(ctx context.Context, reconcileAfter time.Time, minInterval time.Duration, verify bool) (bool, error) {
	capturedAt := time.Now().UTC()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	snapshot, err := unit.readMetricCatalog(ctx)
	if err != nil && !errors.Is(err, ErrMetricCatalogUnavailable) {
		return false, err
	}
	generation, err := unit.metricCatalogGeneration(ctx)
	if err != nil {
		return false, err
	}
	if !verify && !snapshot.BuiltAt.IsZero() && !snapshot.CapturedAt.Before(reconcileAfter) {
		if snapshot.Generation == generation || time.Since(snapshot.BuiltAt) < minInterval {
			return false, nil
		}
	}
	var locked bool
	if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, metricCatalogBuildLock).Scan(&locked); err != nil {
		return false, err
	}
	if !locked {
		return false, ErrMetricCatalogBusy
	}
	metrics, err := unit.listMetricsContext(ctx, compatibilityCatalogSQL())
	if err != nil {
		return false, err
	}
	if verify {
		prior, err := unit.listMetricsContext(ctx, legacyCompatibilityCatalogSQL())
		if err != nil {
			return false, err
		}
		if !slices.Equal(metrics, prior) {
			return false, errors.New("metric catalog differs from legacy SQL")
		}
	}
	payload, err := json.Marshal(newMetricCatalog(capturedAt, generation, metrics))
	if err != nil {
		return false, err
	}
	// Repeatable read fences conflicting publications with a serialization
	// error. The generation row stays independent of the published row.
	_, err = tx.Exec(ctx, `
		INSERT INTO settings (key,value,updated_at) VALUES ($1,$2,clock_timestamp()::text)
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value,updated_at=EXCLUDED.updated_at`,
		metricCatalogKey, string(payload))
	if err != nil {
		return false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

// SeedEmptyMetricCatalog is called during provisioning before activation.
// A retried provision preserves any existing catalog.
func (s *DB) SeedEmptyMetricCatalog(ctx context.Context) error {
	payload, err := json.Marshal(newMetricCatalog(time.Now().UTC(), "0/0", nil))
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO settings (key,value,updated_at)
		SELECT $1,$2,clock_timestamp()::text
		WHERE NOT EXISTS (SELECT 1 FROM metric_points)
		  AND NOT EXISTS (SELECT 1 FROM hourly_metrics)
		  AND NOT EXISTS (SELECT 1 FROM settings WHERE key IN ($3,$4))
		ON CONFLICT (key) DO NOTHING`, metricCatalogKey, string(payload), metricCatalogSourceKey, metricCatalogHoursKey)
	return err
}
