package storage

import (
	"reflect"
	"testing"
	"time"
)

func TestStepHourPriorityAcrossActivityReaders(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	const date = "2026-10-15"
	const previous = "2026-10-14"
	point := func(day, stamp, src string, qty float64) MetricPoint {
		return MetricPoint{MetricName: "step_count", Units: "count", Date: day + " " + stamp + " +0000", Source: src, Qty: qty}
	}
	insertHRVRepairPoints(t, db, []MetricPoint{
		point(previous, "09:00:00", "iPhone", 9),
		point("2026-10-13", "09:00:00", "iPhone", 8),
		point(date, "09:00:00", "Apple Watch", 10),
		point(date, "09:00:20", "Apple Watch", 15),
		point(date, "09:01:00", "Apple Watch", 20),
		point(date, "09:01:00", "iPhone", 1100),
		point(date, "09:01:00", "other", 2000),
		point(date, "19:00:00", "iPhone", 100),
		point(date, "19:00:00", "other", 1001),
	})
	if got := db.GetTodayStepCount(date); got != 135 {
		t.Fatalf("today steps = %v", got)
	}
	raw := db.rawMetricsFromPoints(date)
	fresh := db.freshDayFromRaw(date)
	if raw == nil || len(raw.Steps) == 0 || raw.Steps[0] != 135 || fresh == nil || fresh.steps == nil || *fresh.steps != 135 {
		t.Fatalf("raw/fresh briefing returned %+v / %+v", raw, fresh)
	}
	wake := time.Date(2026, 10, 15, 9, 0, 30, 0, time.UTC)
	now := time.Date(2026, 10, 15, 19, 0, 0, 0, time.UTC)
	if steps, err := postWakeSteps(ctx, db.pool, date, wake, now); err != nil || steps != 120 {
		t.Fatalf("timestamp-bounded wake steps = %v, err %v", steps, err)
	}
	for _, cached := range []bool{false, true} {
		if cached {
			if err := db.upsertHourlySumForDate(date); err != nil {
				t.Fatal(err)
			}
		}
		day, err := db.GetMetricData("step_count", previous, date+" 23:59:59", "day", "SUM")
		if err != nil || !reflect.DeepEqual(day, []DataPoint{{Date: previous, Qty: 9, Min: 9, Max: 9}, {Date: date, Qty: 135, Min: 10, Max: 100}}) {
			t.Fatalf("day cached=%t: %+v, %v", cached, day, err)
		}
		hours, err := db.GetMetricData("step_count", date, date+" 23:59:59", "hour", "SUM")
		if err != nil || !reflect.DeepEqual(hours, []DataPoint{{Date: date + " 09:00", Qty: 35, Min: 10, Max: 20}, {Date: date + " 19:00", Qty: 100, Min: 100, Max: 100}}) {
			t.Fatalf("hours cached=%t: %+v, %v", cached, hours, err)
		}
	}
	early, err := db.GetMetricData("step_count", "2026-10-13", "2026-10-13 23:59:59", "day", "SUM")
	if err != nil || len(early) != 1 || early[0].Date != "2026-10-13" || early[0].Qty != 8 {
		t.Fatalf("raw history before cache: %+v, %v", early, err)
	}
	if got := db.intradayPartialSum(date, "step_count"); got != 135 {
		t.Fatalf("intraday steps = %v", got)
	}
	latest, err := db.latestMetricValues(true)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, metric := range latest {
		if metric.Metric == "step_count" {
			found = metric.Date == date && metric.Value == 135
		}
	}
	if !found {
		t.Fatalf("latest steps: %+v", latest)
	}
	if err := db.refreshDashboardSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	dashboard, err := db.GetDashboard()
	if err != nil || dashboard == nil || len(dashboard.Cards) == 0 || dashboard.Cards[0].Metric != "step_count" || dashboard.Cards[0].Value != 135 {
		t.Fatalf("dashboard steps: %+v, %v", dashboard, err)
	}
}

func TestMeasuredZeroAcrossStepReaders(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	const date = "2026-10-26"
	record, err := db.InsertRaw(Record{ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InsertPoints(record, []MetricPoint{{MetricName: "step_count", Units: "count", Date: date + " 02:00:00 +0100", Source: "Apple Watch", Qty: 0}}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRecentCache([]string{date}, false); err != nil {
		t.Fatal(err)
	}
	fresh := db.freshDayFromRaw(date)
	if fresh == nil || fresh.steps == nil || *fresh.steps != 0 || db.freshDayFromRaw("2026-10-27") != nil {
		t.Fatalf("fresh briefing did not distinguish measured zero from missing: %+v", fresh)
	}
	for _, legacy := range []bool{false, true} {
		values, err := db.LatestMetricsForContract(legacy)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, v := range values {
			if v.Metric == "step_count" {
				found = v.Date == date && v.Value == 0
			}
		}
		if !found {
			t.Fatalf("latest steps legacy=%v: %+v", legacy, values)
		}
	}
	if err := db.refreshDashboardSnapshot(t.Context()); err != nil {
		t.Fatal(err)
	}
	dashboard, err := db.GetDashboard()
	if err != nil || dashboard == nil || len(dashboard.Cards) != 1 || dashboard.Cards[0].Metric != "step_count" || dashboard.Cards[0].Value != 0 || dashboard.Date != date {
		t.Fatalf("zero-step dashboard: %+v, %v", dashboard, err)
	}
}

func TestLatestStepsSkipsIneligibleNewestDay(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	insertHRVRepairPoints(t, db, []MetricPoint{
		{MetricName: "step_count", Units: "count", Date: "2026-10-15 09:00:00 +0000", Source: "iPhone", Qty: 12},
		{MetricName: "step_count", Units: "unknown", Date: "2026-10-16 09:00:00 +0000", Source: "iPhone", Qty: 35},
	})
	var day string
	var steps float64
	if err := db.pool.QueryRow(t.Context(), latestPhysicalStepsSQL()).Scan(&day, &steps); err != nil {
		t.Fatal(err)
	}
	if day != "2026-10-15" || steps != 12 {
		t.Fatalf("latest eligible steps = %s %v", day, steps)
	}
}
