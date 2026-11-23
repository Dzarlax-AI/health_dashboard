package storage

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/testdb"
)

func TestMetricCatalogSnapshotLifecycle(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	if _, err := db.ReadMetricCatalog(ctx, false); !errors.Is(err, ErrMetricCatalogUnavailable) {
		t.Fatalf("unprepared catalog: %v", err)
	}
	if err := db.SeedEmptyMetricCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if rows, err := db.ReadMetricCatalog(ctx, true); err != nil || rows == nil || len(rows) != 0 {
		t.Fatalf("valid empty catalog: %v", err)
	}

	raw, err := db.InsertRaw(Record{ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	addDay := func(day string) {
		t.Helper()
		if err := db.InsertPoints(raw, []MetricPoint{
			{MetricName: "six_minute_walking_test_distance", Units: "m", Date: day + " 12:00:00 +0000", Source: "Fixture", Qty: 500},
			{MetricName: "sleep_total", Units: "hr", Date: day + " 00:00:00 +0000", Source: "Fixture", Qty: 8},
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := db.rebuildRecentAggregateDate(ctx, day, liveDailySQL(false)); err != nil {
			t.Fatal(err)
		}
	}
	addDay("2026-07-01")
	if err := db.PrepareMetricCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{false, true} {
		want, err := db.ListMetricsForContract(legacy)
		if err != nil {
			t.Fatal(err)
		}
		got, err := db.ReadMetricCatalog(ctx, legacy)
		if err != nil || !slices.Equal(got, want) || len(got) < 2 {
			t.Fatalf("catalog parity legacy=%t: %v", legacy, err)
		}
	}

	var schema string
	if err := db.pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	readerPool, err := testdb.NewPool(ctx, testdb.DSN(t), schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readerPool.Close)
	reader := &DB{pool: readerPool}
	initial, err := reader.readMetricCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	checkSnapshot := func(expected metricCatalogSnapshot) {
		t.Helper()
		current, err := reader.readMetricCatalog(ctx)
		if err != nil || current.Generation != expected.Generation ||
			!current.BuiltAt.Equal(expected.BuiltAt) || !slices.Equal(current.Metrics, expected.Metrics) {
			t.Fatalf("catalog changed before publication: %v", err)
		}
	}

	// The write happens after the builder has observed its generation and
	// after its first catalog query. Both tables must still reflect day 1.
	gated := &catalogTestPool{storagePool: db.pool, queried: make(chan struct{}), resume: make(chan struct{})}
	builder := &DB{pool: gated}
	buildCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	reconcileAfter := time.Now()
	buildDone := make(chan error, 1)
	go func() {
		_, err := builder.RefreshMetricCatalog(buildCtx, reconcileAfter, 0)
		buildDone <- err
	}()
	select {
	case <-gated.queried:
	case err := <-buildDone:
		t.Fatalf("builder exited before reading catalog inputs: %v", err)
	case <-buildCtx.Done():
		t.Fatal(buildCtx.Err())
	}
	addDay("2026-07-02")
	checkSnapshot(initial)
	if _, err := reader.RefreshMetricCatalog(ctx, reconcileAfter, 0); !errors.Is(err, ErrMetricCatalogBusy) {
		t.Fatalf("competing builder took installation lock: %v", err)
	}
	close(gated.resume)
	if err := <-buildDone; err != nil {
		t.Fatal(err)
	}
	inFlight, err := reader.readMetricCatalog(ctx)
	if err != nil || inFlight.Generation != initial.Generation || !slices.Equal(inFlight.Metrics, initial.Metrics) {
		t.Fatalf("in-flight publication mixed source generations: %v", err)
	}
	if built, err := reader.RefreshMetricCatalog(ctx, reconcileAfter, time.Hour); err != nil || built {
		t.Fatalf("generation bypassed coalescing: built=%t %v", built, err)
	}

	// Make the exact builder fail while settings remain available.
	if _, err := db.pool.Exec(ctx, `ALTER TABLE hourly_metrics RENAME TO catalog_unavailable_hours`); err != nil {
		t.Fatal(err)
	}
	_, buildErr := reader.RefreshMetricCatalog(ctx, reconcileAfter, 0)
	if _, err := db.pool.Exec(ctx, `ALTER TABLE catalog_unavailable_hours RENAME TO hourly_metrics`); err != nil {
		t.Fatal(err)
	}
	if buildErr == nil {
		t.Fatal("builder accepted missing input")
	}
	checkSnapshot(inFlight)
	if built, err := reader.RefreshMetricCatalog(ctx, reconcileAfter, 0); err != nil || !built {
		t.Fatalf("new generation was lost: built=%t %v", built, err)
	}
	current, err := reader.readMetricCatalog(ctx)
	if err != nil || current.Generation == initial.Generation || len(current.Metrics) != len(initial.Metrics) {
		t.Fatalf("publish after retry: %v", err)
	}
	for _, row := range current.Metrics {
		if row.Count != 2 || row.Min != "2026-07-01" || row.Max != "2026-07-02" {
			t.Fatal("publish lost a source hour or history bound")
		}
	}
	if built, err := db.RefreshMetricCatalog(ctx, reconcileAfter, 0); err != nil || built {
		t.Fatalf("unchanged generation was rebuilt: built=%t %v", built, err)
	}

	// A rolled back write preserves the durable generation.
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := MarkMetricCatalogHoursChangedTx(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if built, err := db.RefreshMetricCatalog(ctx, reconcileAfter, 0); err != nil || built {
		t.Fatalf("rolled back generation became visible: built=%t %v", built, err)
	}
	db.InvalidateDateRangeAggregates("2026-07-01", "2026-07-01")
	if built, err := reader.RefreshMetricCatalog(ctx, reconcileAfter, 0); err != nil || !built {
		t.Fatalf("aggregate deletion not tracked: built=%t %v", built, err)
	}
	// Simulate an aggregate rebuilt by the old binary during a rollback.
	if _, err := db.pool.Exec(ctx, `UPDATE hourly_metrics SET hour='2026-07-03 00:00' WHERE metric_name='sleep_total'`); err != nil {
		t.Fatal(err)
	}
	if built, err := reader.RefreshMetricCatalog(ctx, reconcileAfter, 0); err != nil || built {
		t.Fatalf("unchanged generation was unexpectedly rebuilt: built=%t %v", built, err)
	}
	if built, err := reader.RefreshMetricCatalog(ctx, time.Now(), time.Hour); err != nil || !built {
		t.Fatalf("startup reconciliation missed untracked data: built=%t %v", built, err)
	}
	rows, err := reader.ReadMetricCatalog(ctx, false)
	if err != nil || !slices.ContainsFunc(rows, func(row MetricSummary) bool {
		return row.Name == "sleep_total" && row.Min == "2026-07-03" && row.Max == "2026-07-03"
	}) {
		t.Fatalf("startup catalog missed the old binary's cache update: %v", err)
	}
	if err := db.SaveSettings(map[string]string{metricCatalogKey: "{"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.ReadMetricCatalog(ctx, false); !errors.Is(err, ErrMetricCatalogUnavailable) {
		t.Fatalf("corrupt catalog was exposed: %v", err)
	}
	if built, err := reader.RefreshMetricCatalog(ctx, reconcileAfter, 0); err != nil || !built {
		t.Fatalf("corrupt snapshot not rebuilt: built=%t %v", built, err)
	}
	readerPool.Close()
	if _, err := reader.ReadMetricCatalog(ctx, false); err == nil || errors.Is(err, ErrMetricCatalogUnavailable) {
		t.Fatalf("database outage was confused with missing snapshot: %v", err)
	}
}

type catalogTestPool struct {
	storagePool
	queried, resume chan struct{}
}

func (p *catalogTestPool) BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error) {
	tx, err := p.storagePool.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &catalogTestTx{Tx: tx, queried: p.queried, resume: p.resume}, nil
}

type catalogTestTx struct {
	pgx.Tx
	queried, resume chan struct{}
}

func (tx *catalogTestTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if sql == compatibilityCatalogSQL() {
		close(tx.queried)
		select {
		case <-tx.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return tx.Tx.Query(ctx, sql, args...)
}
