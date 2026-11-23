package storage

import "testing"

// The admin force rebuild retains daily_scores while rebuilding its inputs.
// A clean reconstruction has no prior sleep block to retain.
func TestForceBackfillPreservesPriorIncompleteSleepBlock(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	const day = "2026-01-15"
	id := insertTestRawRecord(t, db, "synthetic-sleep-rebuild")
	stage := func(metric, source string, qty float64) MetricPoint {
		return MetricPoint{MetricName: metric, Date: day + " 00:00:00 +0000", Source: source, Units: "hr", Qty: qty}
	}
	if err := db.InsertPoints(id, []MetricPoint{stage("sleep_total", "Apple Watch", 7),
		stage("sleep_deep", "Apple Watch", 1), stage("sleep_rem", "Apple Watch", 2),
		stage("sleep_core", "Apple Watch", 3.5), stage("sleep_awake", "Apple Watch", 0.5)}); err != nil {
		t.Fatal(err)
	}
	check := func(want *float64) {
		t.Helper()
		if err := db.BackfillAggregates(true); err != nil {
			t.Fatal(err)
		}
		var total, deep *float64
		if err := db.pool.QueryRow(t.Context(), `select sleep_total,sleep_deep from daily_scores where date=$1`, day).Scan(&total, &deep); err != nil {
			t.Fatal(err)
		}
		if want == nil {
			if total != nil || deep != nil {
				t.Fatal("clean incomplete candidate produced a sleep block")
			}
			return
		}
		if total == nil || *total != *want || deep == nil || *deep != 1 {
			t.Fatalf("prior staged night was lost: total %v, deep %v", total, deep)
		}
	}
	seven := 7.0
	check(&seven)
	if err := db.InsertPoints(id, []MetricPoint{stage("sleep_total", "Other", 4.9)}); err != nil {
		t.Fatal(err)
	}
	check(&seven)
	if _, err := db.pool.Exec(t.Context(), `delete from daily_scores where date=$1`, day); err != nil {
		t.Fatal(err)
	}
	check(nil)
}
