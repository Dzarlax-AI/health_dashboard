package storage

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestIngestCacheReadyPreservesAggregatesWithoutSecondRebuild(t *testing.T) {
	for _, recompute := range []bool{true, false} {
		name := "score-metrics"
		if !recompute {
			name = "steps-only"
		}
		t.Run(name, func(t *testing.T) { checkIngestCacheReadyParity(t, recompute) })
	}
}

func checkIngestCacheReadyParity(t *testing.T, recompute bool) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var recordID int64
	if err := db.pool.QueryRow(ctx, `INSERT INTO health_records (payload) VALUES ('{}') RETURNING id`).Scan(&recordID); err != nil {
		t.Fatal(err)
	}
	points := []MetricPoint{
		{MetricName: "step_count", Date: "2026-09-29 12:00:00 +0000", Qty: 3000, Source: "Apple Watch", Units: "count"},
		{MetricName: "heart_rate_variability", Date: "2026-09-29 07:00:00 +0000", Qty: 45, Source: "Apple Watch", Units: "ms"},
		{MetricName: "resting_heart_rate", Date: "2026-09-29 07:00:00 +0000", Qty: 60, Source: "Apple Watch", Units: "bpm"},
		{MetricName: "sleep_total", Date: "2026-09-29 07:00:00 +0000", Qty: 7, Source: "Apple Watch", Units: "hr"},
	}
	if err := db.InsertPoints(recordID, points); err != nil {
		t.Fatal(err)
	}
	dates := []string{"2026-09-29"}
	snapshot := func() (string, error) {
		var value string
		err := db.pool.QueryRow(ctx, `SELECT json_agg(row_to_json(d) ORDER BY date)::text FROM
			(SELECT date, readiness, score_version, hrv_avg, rhr_avg, sleep_total, steps FROM daily_scores) d`).Scan(&value)
		return value, err
	}
	// A non-score upload updates steps over an existing scored day.
	if !recompute {
		if err := db.UpsertRecentCache(dates, true); err != nil {
			t.Fatal(err)
		}
		if err := db.InsertPoints(recordID, []MetricPoint{{MetricName: "step_count", Date: "2026-09-29 12:00:00 +0000", Qty: 4500, Source: "Apple Watch", Units: "count"}}); err != nil {
			t.Fatal(err)
		}
	}
	// The optimized path performs exactly one inline aggregate pass.
	if err := db.UpsertRecentCache(dates, recompute); err != nil {
		t.Fatal(err)
	}
	var optimized string
	var repeated atomic.Int32
	coordinator := NewTodayDerivedStateCoordinator(nil)
	coordinator.refreshEnergy = func(context.Context, *DB, string, string) bool { return true }
	done := make(chan error, 1)
	coordinator.TriggerRefresh(ctx, db, "synthetic", TodayDerivedRefresh{Dates: dates, CacheReady: true},
		func(dates []string) error { repeated.Add(1); return db.UpsertRecentCache(dates, true) },
		nil, func() string { return "UTC" }, func() error {
			got, err := snapshot()
			optimized = got
			done <- err
			return nil
		})
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Today pass did not complete")
	}
	if repeated.Load() != 0 {
		t.Fatalf("repeated aggregate passes: %d", repeated.Load())
	}
	// Compare with the legacy unconditional second pass on exactly the same input.
	if err := db.UpsertRecentCache(dates, true); err != nil {
		t.Fatal(err)
	}
	legacy, err := snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if optimized != legacy {
		t.Fatalf("aggregate mismatch: optimized %s legacy %s", optimized, legacy)
	}

}
