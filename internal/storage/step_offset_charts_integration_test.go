package storage

import "testing"

func TestStepChartsCountPhysicalInstantsAcrossOffsetSpellings(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	id, err := db.InsertRaw(Record{ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	const day = "2026-10-25"
	var points []MetricPoint
	for _, stamp := range []string{
		"2026-10-24 22:15:00 +0000", day + " 00:15:00 +0200",
		day + " 00:30:00 +0000", day + " 02:30:00 +0200",
		day + " 01:30:00 +0000", day + " 02:30:00 +0100",
	} {
		points = append(points, MetricPoint{MetricName: "step_count", Units: "count", Date: stamp, Source: "Apple Watch", Qty: 11})
	}
	if err := db.InsertPoints(id, points); err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{true, false} {
		for _, bucket := range []string{"minute", "hour", "day"} {
			for _, window := range []struct {
				day  string
				want float64
			}{
				{"2026-10-24", 0}, {day, 33},
			} {
				from, to := window.day, window.day+" 23:59:59"
				check := func(rows []DataPoint) {
					t.Helper()
					var sum float64
					for _, row := range rows {
						sum += row.Qty
					}
					if sum != window.want || (window.want == 0 && len(rows) != 0) {
						t.Fatalf("legacy=%t day=%s bucket=%s rows=%v want total %v", legacy, from, bucket, rows, window.want)
					}
				}
				rows, err := db.MetricDataForContract("step_count", from, to, bucket, "SUM", legacy)
				if err != nil {
					t.Fatal(err)
				}
				check(rows)
				sources, err := db.MetricDataBySourceForContract("step_count", from, to, bucket, "SUM", legacy)
				if err != nil {
					t.Fatal(err)
				}
				var series []DataPoint
				for _, source := range sources {
					if source.Source != "Apple Watch" {
						t.Fatal(source.Source)
					}
					series = append(series, source.Points...)
				}
				check(series)
			}
		}
	}
}
