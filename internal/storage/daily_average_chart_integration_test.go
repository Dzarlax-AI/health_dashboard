package storage

import "testing"

func TestDailyAverageChartUsesEqualSourceWeights(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	const date = "2026-10-15"
	cases := []struct {
		metric, unit string
		base         float64
	}{
		{"resting_heart_rate", "bpm", 60},
		{"blood_oxygen_saturation", "%", 90},
		{"vo2_max", "mL/kg/min", 35},
		{"respiratory_rate", "count/min", 12},
	}
	var points []MetricPoint
	for _, c := range cases {
		for _, hour := range []struct {
			stamp, source string
			delta         float64
		}{
			{"09:00", "A", 0}, {"10:00", "A", 2}, {"09:00", "B", 7},
		} {
			points = append(points, MetricPoint{MetricName: c.metric, Units: c.unit, Date: date + " " + hour.stamp + ":00 +0000", Source: hour.source, Qty: c.base + hour.delta})
		}
	}
	insertHRVRepairPoints(t, db, points)
	for _, state := range []string{"raw", "cached"} {
		if state == "cached" {
			if err := db.upsertHourlyAvgForDate(date); err != nil {
				t.Fatal(err)
			}
		}
		for _, c := range cases {
			got, err := db.GetMetricData(c.metric, date, date+" 23:59:59", "day", "AVG")
			if err != nil || len(got) != 1 || got[0].Qty != c.base+4 || got[0].Min != c.base || got[0].Max != c.base+7 {
				t.Fatalf("%s %s: got %+v, err %v", state, c.metric, got, err)
			}
			perSource, err := db.GetMetricDataBySource(c.metric, date, date+" 23:59:59", "day", "AVG")
			if err != nil || len(perSource) != 2 {
				t.Fatalf("%s %s per source: %+v, err %v", state, c.metric, perSource, err)
			}
			want := map[string]float64{"A": c.base + 1, "B": c.base + 7}
			for _, series := range perSource {
				expected, ok := want[series.Source]
				if !ok || len(series.Points) != 1 || series.Points[0].Qty != expected {
					t.Fatalf("%s %s per source: %+v", state, c.metric, series)
				}
			}
			minutes, err := db.GetMetricData(c.metric, date, date+" 23:59:59", "minute", "AVG")
			if err != nil || len(minutes) != 2 || minutes[0].Qty != c.base+3.5 || minutes[1].Qty != c.base+2 {
				t.Fatalf("%s %s minute: %+v, err %v", state, c.metric, minutes, err)
			}

		}
	}
}

func TestRawChartWholeDayIncludesFinalSecond(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	const day = "2026-10-15"
	const next = "2026-10-16"
	for _, c := range []struct{ metric, unit, bucket, agg string }{
		{"heart_rate", "bpm", "hour", "AVG"},
		{"resting_heart_rate", "bpm", "day", "AVG"},
		{"active_energy", "kcal", "minute", "SUM"},
		{"step_count", "count", "minute", "SUM"},
	} {
		t.Run(c.metric, func(t *testing.T) {
			insertHRVRepairPoints(t, db, []MetricPoint{
				{MetricName: c.metric, Units: c.unit, Source: "Watch", Date: day + " 23:59:59 +0100", Qty: 10},
				{MetricName: c.metric, Units: c.unit, Source: "Watch", Date: next + " 00:00:00 +0100", Qty: 90},
			})
			for _, to := range []string{day + " 23:59:59", day + " 23:59:59 +0100", next} {
				points, err := db.metricDataRaw(c.metric, day, to, c.bucket, c.agg)
				if err != nil || len(points) != 1 || points[0].Qty != 10 {
					t.Fatalf("combined through %q: %+v, %v", to, points, err)
				}
				sources, err := db.metricDataBySourceRaw(c.metric, day, to, c.bucket, c.agg)
				if err != nil || len(sources) != 1 || len(sources[0].Points) != 1 || sources[0].Points[0].Qty != 10 {
					t.Fatalf("per source through %q: %+v, %v", to, sources, err)
				}
			}
		})
	}
}
