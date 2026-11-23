package storage

import (
	"os"
	"reflect"
	"testing"
	"time"
)

// Exercise both capture adapters and compare each public field to the prior
// algorithm. All timestamps have exact binary fractional-hour durations.
func TestSleepArchitectureShadow(t *testing.T) {
	if os.Getenv("BRIEFING_STORAGE_PILOT") != "1" {
		t.Skip("synthetic dbt capture required")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	id := insertTestRawRecord(t, db, "architecture-synthetic")
	const first = "2026-10-19"
	const last = "2026-11-17"
	raw := []SleepArchitectureRawSegment{
		{"2026-10-18 23:30:00 +0200", "sleep_core", "Apple Watch", 2},
		{"2026-10-19 01:45:00 +0200", "sleep_rem", "Apple Watch", 1},
		{"2026-10-19 01:20:00 +0200", "sleep_awake", "Apple Watch", 0.25},
		{"2026-10-19 01:25:00 +0200", "sleep_awake", "Apple Watch", 0.125},
		{"2026-10-19 03:15:00.000000001 +0200", "sleep_deep", "Apple Watch", 1},
		{"2026-10-19 02:00:00 +0200", "sleep_core", "RingConn", 6},
		{"2026-10-20 02:00:00 +0200", "sleep_unspecified", "RingConn", 5},
		{"2026-10-21 15:00:00 +0230", "sleep_core", "RingConn", 2},
		{"2026-10-25 00:30:00 +0200", "sleep_core", "Watch", 1},
		{"2026-10-25 01:00:00 +0100", "sleep_deep", "Watch", 1},
		{"2026-11-03 04:00:00 -0430", "sleep_core", "Phone", 1},
	}
	anchor, _ := time.Parse(isoDate, "2026-11-03")
	for i := 1; i <= 14; i++ {
		raw = append(raw, SleepArchitectureRawSegment{anchor.AddDate(0, 0, i).Format(isoDate) + " 03:30:00 +0230", "sleep_core", "Phone", 2})
	}
	for i, r := range raw {
		_, err := db.pool.Exec(ctx, `insert into metric_points(health_record_id,metric_name,date,qty,source,quality) values ($1,$2,$3,$4,$5,'ok')`, id, r.MetricName, r.Date, r.Hours, r.Source)
		must(err)
		start, err := parseSleepDate(r.Date)
		must(err)
		end := start.Add(time.Duration(r.Hours * float64(time.Hour)))
		// First recorded offset for the end date is +0200 for the offset switch.
		loc := end.Location()
		if end.Format(isoDate) == "2026-10-25" {
			loc = time.FixedZone("first", 7200)
		}
		origin := time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, loc)
		_, err = db.pool.Exec(ctx, `insert into briefing_pilot.architecture_segments select 'synthetic',capture,$1,$2,$3,$4,$5,$6,$7 from unnest(array['automatic','preferred']) capture`, end.Format(isoDate), i, r.MetricName, r.Source, int64(start.Sub(origin)), int64(end.Sub(origin)), r.Hours)
		must(err)
	}
	for _, tc := range []struct{ capture, preferred string }{{"automatic", ""}, {"preferred", "RingConn"}} {
		_, err := db.pool.Exec(ctx, `insert into briefing_pilot.architecture_captures values ('synthetic',$1,$2,$3,$4)`, tc.capture, first, last, tc.preferred)
		must(err)
		must(db.SaveSettings(map[string]string{PreferredSleepSourceSetting: tc.preferred}))
		old := computeSleepArchitectureDays(raw, tc.preferred)
		live, err := db.LoadSleepArchitectureDays(first, last)
		must(err)
		if !reflect.DeepEqual(live, old) {
			t.Fatalf("%s days: live %+v; previous %+v", tc.capture, live, old)
		}
		rows, err := db.pool.Query(ctx, `select day,source,asleep_hours,waso,explicit_bouts,gaps,longest,fragmentation,eligible,reason,confidence from derived.sleep_architecture_days_v1 where capture=$1`, tc.capture)
		must(err)
		shadow := map[string]SleepArchitectureDay{}
		for rows.Next() {
			var d SleepArchitectureDay
			must(rows.Scan(&d.Date, &d.Source, &d.AsleepHours, &d.WASOHours, &d.ExplicitWakeBouts, &d.GapInferredWakeBouts, &d.LongestWakeBoutHours, &d.FragmentationIndex, &d.Eligible, &d.Reason, &d.Confidence))
			shadow[d.Date] = d
		}
		must(rows.Err())
		rows.Close()
		if !reflect.DeepEqual(old, shadow) {
			t.Fatalf("%s dbt days %+v, previous %+v", tc.capture, shadow, old)
		}
		windows, err := db.LoadSleepArchitectureFeatures(first, last)
		must(err)
		// The second batch starts on November 8 and must still include the
		// preceding batch's eligible days in its first window.
		extended, err := db.LoadSleepArchitectureFeatures("2026-08-10", last)
		must(err)
		for anchor, w := range windows {
			if !reflect.DeepEqual(extended[anchor], w) {
				t.Fatalf("batch boundary changed %s", anchor)
			}
		}
		rows, err = db.pool.Query(ctx, `select anchor,days,eligible_days,source_days,reasons,confidence,waso,explicit_bouts,gaps,fragmentation,longest from derived.sleep_architecture_windows_v1 where capture=$1 order by anchor,days`, tc.capture)
		must(err)
		var count int
		for rows.Next() {
			var a string
			var w SleepArchitectureWindow
			must(rows.Scan(&a, &w.Days, &w.EligibleDays, &w.SourceDays, &w.MissingReasonCounts, &w.Confidence, &w.WASOHours, &w.ExplicitWakeBouts, &w.GapInferredWakeBouts, &w.FragmentationIndex, &w.LongestWakeBoutHours))
			anchor, err := time.Parse(isoDate, a)
			must(err)
			if want := BuildSleepArchitectureWindow(anchor, w.Days, old); !reflect.DeepEqual(w, want) {
				t.Fatalf("%s %s/%d window %+v; expected %+v", tc.capture, a, w.Days, w, want)
			}
			if want := BuildSleepArchitectureFeatureFields(anchor, old); !reflect.DeepEqual(windows[a], want) {
				t.Fatalf("%s %s storage features %+v, expected %+v", tc.capture, a, windows[a], want)
			}
			count++
		}
		must(rows.Err())
		rows.Close()
		if count != 60 || len(windows) != 30 {
			t.Fatalf("window coverage: %d, %d", count, len(windows))
		}
	}
}
