package storage

import (
	"context"
	"testing"
)

// Historical candidates are a point-in-time corpus. A later sync must never
// change a candidate's metric window just because metric_points retains both
// days. This covers the cache supplement (night sleep/nap), raw fallback, and
// wrist-temperature fallback used by BuildHistoricalDailyInsightSnapshot.
func TestHistoricalDailyInsightMetricWindowsExcludeFutureDates(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	ctx := context.Background()
	recordID := insertTestRawRecord(t, db, "historical-insight-bounds")
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO metric_points (health_record_id, metric_name, units, date, qty, source, quality) VALUES
			($1, 'night_sleep_total', 'hr', '2026-09-01 08:00:00 +0000', 7, 'Apple Watch', 'ok'),
			($1, 'night_sleep_total', 'hr', '2026-09-02 08:00:00 +0000', 99, 'Apple Watch', 'ok'),
			($1, 'nap_total', 'hr', '2026-09-01 14:00:00 +0000', 1, 'Apple Watch', 'ok'),
			($1, 'nap_total', 'hr', '2026-09-02 14:00:00 +0000', 99, 'Apple Watch', 'ok'),
			($1, 'heart_rate_variability', 'ms', '2026-09-01 08:00:00 +0000', 50, 'Apple Watch', 'ok'),
			($1, 'heart_rate_variability', 'ms', '2026-09-02 08:00:00 +0000', 999, 'Apple Watch', 'ok'),
			($1, 'wrist_temperature', 'C', '2026-09-01 08:00:00 +0000', 0.5, 'Apple Watch', 'ok'),
			($1, 'wrist_temperature', 'C', '2026-09-02 08:00:00 +0000', 99, 'Apple Watch', 'ok')`, recordID); err != nil {
		t.Fatalf("seed metric points: %v", err)
	}
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO daily_scores (date, hrv_avg, sleep_total)
		VALUES ('2026-09-01', 50, 7)`); err != nil {
		t.Fatalf("seed daily score: %v", err)
	}

	cached := db.rawMetricsFromDailyScoresAt("2026-09-01", false)
	if cached == nil || len(cached.NightSleep) != 1 || cached.NightSleep[0] != 7 || len(cached.Nap) != 1 || cached.Nap[0] != 1 {
		t.Fatalf("cache-backed historical raw metrics included future sleep data: %#v", cached)
	}
	if got := db.fetchDailyMetric("wrist_temperature", "2026-09-01", 30, "AVG"); len(got) != 1 || got[0] != 0.5 {
		t.Fatalf("historical wrist temperature = %#v, want only 2026-09-01 value", got)
	}

	raw := db.rawMetricsFromPoints("2026-09-01")
	if raw == nil || len(raw.HRV) != 1 || raw.HRV[0] != 50 {
		t.Fatalf("raw historical HRV = %#v, want only 2026-09-01 value", raw)
	}
	for _, day := range raw.Daily {
		if day.Date > "2026-09-01" {
			t.Fatalf("raw historical window leaked future day %q", day.Date)
		}
	}
}
