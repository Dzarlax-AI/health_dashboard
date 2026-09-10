package storage

import (
	"context"
	"testing"
)

func TestGetDashboardServesOnlyAnAtomicallyCompletedSnapshot(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	ctx := context.Background()
	var recordID int64
	err := db.pool.QueryRow(ctx, `
		INSERT INTO health_records (received_at, processing_status)
		VALUES ('2026-09-10T16:30:00+02:00', 'complete')
		RETURNING id`).Scan(&recordID)
	if err != nil {
		t.Fatalf("seed health record: %v", err)
	}
	_, err = db.pool.Exec(ctx, `
		INSERT INTO metric_points (health_record_id, metric_name, units, date, qty, source, quality) VALUES
			($1, 'step_count', 'count', '2026-09-10 08:00:00 +02:00', 4000, 'Apple Watch', 'ok'),
			($1, 'heart_rate', 'bpm', '2026-09-10 08:00:00 +02:00', 60, 'Apple Watch', 'ok'),
			($1, 'walking_running_distance', 'mi', '2026-09-10 08:00:00 +02:00', 2, 'Apple Watch', 'ok')`, recordID)
	if err != nil {
		t.Fatalf("seed metric points: %v", err)
	}
	_, err = db.pool.Exec(ctx, `
		INSERT INTO hourly_metrics (metric_name, hour, source, avg_val, sample_count) VALUES
			('step_count', '2026-09-09 12:00', 'Apple Watch', 7000, 1),
			('step_count', '2026-09-10 08:00', 'Apple Watch', 4000, 1),
			('step_count', '2026-09-10 16:00', 'Apple Watch', 4000, 1),
			('step_count', '2026-09-10 08:00', 'iPhone', 9999, 1),
			('heart_rate', '2026-09-09 12:00', 'Apple Watch', 64, 2),
			('heart_rate', '2026-09-10 08:00', 'Apple Watch', 60, 9),
			('heart_rate', '2026-09-10 16:00', 'iPhone', 80, 1),
			('walking_running_distance', '2026-09-10 08:00', 'Apple Watch', 2, 1),
			('sleep_total', '2026-09-09 08:00', 'Apple Watch', 7, 1),
			('sleep_total', '2026-09-10 08:00', 'Apple Watch', 7, 1),
			('sleep_total', '2026-09-10 08:00', 'RingConn', 12, 1),
			('resting_heart_rate', '2026-09-09 08:00', 'Apple Watch', 55, 1)
	`)
	if err != nil {
		t.Fatalf("seed hourly cache: %v", err)
	}

	if err := db.refreshDashboardSnapshot(ctx); err != nil {
		t.Fatalf("refresh dashboard snapshot: %v", err)
	}
	initial, err := db.GetDashboard()
	if err != nil {
		t.Fatalf("read completed dashboard snapshot: %v", err)
	}
	if initial.CacheState != dashboardCacheStateComplete || initial.CacheCompletedAt == "" {
		t.Fatalf("initial cache metadata = (%q, %q), want completed snapshot", initial.CacheState, initial.CacheCompletedAt)
	}

	// Simulate a partially-written next generation. Readers must retain the
	// last completed response until the writer finishes and commits its replace.
	if _, err := db.pool.Exec(ctx, `DELETE FROM hourly_metrics WHERE metric_name = 'step_count'`); err != nil {
		t.Fatalf("begin partial cache refresh: %v", err)
	}
	finishRefresh := db.BeginDashboardRefresh()
	defer finishRefresh()
	got, err := db.GetDashboard()
	if err != nil {
		t.Fatalf("GetDashboard: %v", err)
	}
	if got.Date != "2026-09-10" {
		t.Fatalf("dashboard date = %q, want latest hourly cache day", got.Date)
	}
	if got.CacheState != dashboardCacheStateUpdating || got.CacheCompletedAt != initial.CacheCompletedAt {
		t.Fatalf("cache metadata = (%q, %q), want retained updating snapshot %q", got.CacheState, got.CacheCompletedAt, initial.CacheCompletedAt)
	}
	var wantLastUpdated string
	if err := db.pool.QueryRow(ctx, `SELECT MAX(received_at) FROM health_records`).Scan(&wantLastUpdated); err != nil {
		t.Fatalf("read expected receipt timestamp: %v", err)
	}
	if got.LastUpdated != wantLastUpdated {
		t.Fatalf("last updated = %q, want preserved health receipt timestamp %q", got.LastUpdated, wantLastUpdated)
	}

	cards := make(map[string]CardData, len(got.Cards))
	for _, card := range got.Cards {
		cards[card.Metric] = card
	}
	if card := cards["step_count"]; card.Value != 8000 || card.Prev != 7000 {
		t.Fatalf("step_count = %#v, want Apple Watch source totals", card)
	}
	if card := cards["heart_rate"]; card.Value != 62 || card.Prev != 64 || card.Unit != "bpm" {
		t.Fatalf("heart_rate = %#v, want weighted cache average with source unit", card)
	}
	if card := cards["walking_running_distance"]; card.Unit != "mi" {
		t.Fatalf("distance = %#v, want preserved source unit rather than a canonical label", card)
	}
	if card := cards["sleep_total"]; card.Value != 7 || card.Prev != 7 {
		t.Fatalf("sleep_total = %#v, want conservative cross-validated cache value", card)
	}
	if card := cards["resting_heart_rate"]; card.Value != 55 || card.Prev != 55 {
		t.Fatalf("resting_heart_rate = %#v, want prior cached value for slow metric", card)
	}
}
