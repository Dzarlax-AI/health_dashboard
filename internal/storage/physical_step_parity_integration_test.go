package storage

import (
	"testing"
	"time"
)

// Exercise the public write paths so a projection that works only before
// cache refresh cannot turn a partial sync into a new historical total.
func TestPhysicalStepParityThroughRefreshAndRebuild(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	const fold, zero, legacy = "2026-10-25", "2026-10-26", "2026-10-27"
	const oldHour = legacy + " 13:00"
	const ambiguous = "2026-10-28"
	if _, err := db.pool.Exec(t.Context(), `insert into hourly_metrics
		(metric_name,hour,source,avg_val,min_val,max_val,sample_count)
		values ('step_count',$1,'iPhone',80,80,80,1)`, oldHour); err != nil {
		t.Fatal(err)
	}
	record, err := db.InsertRaw(Record{ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	point := func(stamp, device string, value float64) MetricPoint {
		return MetricPoint{MetricName: "step_count", Units: "count", Date: stamp, Source: device, Qty: value}
	}
	partial := point(oldHour+":00 +0100", "Apple Watch", 5)
	if err := db.InsertPoints(record, []MetricPoint{
		point(fold+" 02:00:00 +0200", "Apple Watch", 40),
		point(fold+" 02:00:00 +0100", "iPhone", 70),
		point(zero+" 03:00:00 +0100", "Apple Watch", 0),
		partial,
		// Legacy observations can lack an offset. The phone's known offset
		// does not establish a separate physical hour for the watch.
		point(ambiguous+" 02:00:00", "Apple Watch", 15),
		point(ambiguous+" 02:00:00 +0100", "iPhone", 70),
	}); err != nil {
		t.Fatal(err)
	}
	check := func(day string, want float64) {
		t.Helper()
		var stored *float64
		if err := db.pool.QueryRow(t.Context(), "select steps from daily_scores where date=$1", day).Scan(&stored); err != nil || stored == nil {
			t.Fatalf("daily_scores[%s] missing, err=%v, want %v", day, err, want)
		}
		if *stored != want {
			t.Fatalf("daily_scores[%s] = %v, want %v", day, *stored, want)
		}
		for _, v1 := range []bool{false, true} {
			rows, err := db.MetricDataForContract("step_count", day, day+" 23:59:59", "day", "SUM", v1)
			if err != nil || len(rows) != 1 || rows[0].Qty != want {
				t.Fatalf("day=%s legacy=%t rows=%v err=%v want=%v", day, v1, rows, err, want)
			}
		}
	}
	uncached, err := db.MetricDataForContract("step_count", ambiguous, ambiguous+" 23:59:59", "day", "SUM", false)
	if err != nil || len(uncached) != 1 || uncached[0].Qty != 15 {
		t.Fatalf("mixed known and unknown offsets before publication: %v, %v", uncached, err)
	}
	// A local timestamp without an offset cannot confirm movement after an
	// absolute wake instant. Only the phone's 01:00 UTC reading is timed.
	windowStart := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
	timed, err := postWakeSteps(t.Context(), db.pool, ambiguous, windowStart, windowStart.Add(3*time.Hour))
	if err != nil || timed != 70 {
		t.Fatalf("steps with a known post-wake timestamp = %v, %v", timed, err)
	}
	dates := []string{fold, zero, legacy, ambiguous}
	for _, refresh := range []func() error{
		func() error { return db.UpsertRecentCache(dates, false) },
		func() error {
			if err := db.InsertPoints(record, []MetricPoint{partial}); err != nil {
				return err
			}
			return db.UpsertRecentCache(dates, false)
		},
		func() error { return db.BackfillAggregates(true) },
	} {
		if err := refresh(); err != nil {
			t.Fatal(err)
		}
		check(fold, 110)
		check(zero, 0)
		check(legacy, 80)
		check(ambiguous, 15)
	}
	// Even with the old iPhone value back, a Watch sample without an offset
	// cannot be assigned to a different physical hour than that value.
	if err := db.InsertPoints(record, []MetricPoint{
		point(oldHour+":00 +0100", "iPhone", 80),
		point(oldHour+":10", "Apple Watch", 5),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRecentCache([]string{legacy}, false); err != nil {
		t.Fatal(err)
	}
	check(legacy, 80)
}
