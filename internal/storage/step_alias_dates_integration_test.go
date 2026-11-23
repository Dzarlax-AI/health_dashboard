package storage

import (
	"slices"
	"testing"
	"time"
)

// Crossing midnight changes both the published date and the immutable input's
// local label. A retry must report the same affected dates without replaying steps.
func TestStepAliasRefreshReattributesBothDates(t *testing.T) {
	const before = "2026-10-24"
	const after = "2026-10-25"
	for _, localFirst := range []bool{false, true} {
		name := "utc_first"
		if localFirst {
			name = "local_first"
		}
		t.Run(name, func(t *testing.T) {
			db, cleanup := testDB(t)
			defer cleanup()
			point := func(stamp string, qty float64) MetricPoint {
				return MetricPoint{MetricName: "step_count", Units: "count", Source: "Watch", Date: stamp, Qty: qty}
			}
			utc := point(before+" 22:00:00 +0000", 80)
			local := point(after+" 00:00:00 +0200", 80)
			accepted := func(p MetricPoint, automation string) []string {
				t.Helper()
				id, err := db.InsertRaw(Record{AutomationName: automation, ContentType: "application/json; metrics-contract=2", Payload: "{}"})
				if err != nil {
					t.Fatal(err)
				}
				_, dates, err := db.InsertAcceptedPointsWithDates(id, []MetricPoint{p}, nil)
				if err != nil {
					t.Fatal(err)
				}
				if err = db.UpsertRecentCache(dates, false); err != nil {
					t.Fatal(err)
				}
				return dates
			}
			assertDay := func(day string, want *float64) {
				t.Helper()
				var got *float64
				if err := db.pool.QueryRow(t.Context(), `select (select steps from daily_scores where date=$1)`, day).Scan(&got); err != nil {
					t.Fatal(err)
				}
				if (want == nil) != (got == nil) || (want != nil && *got != *want) {
					t.Fatalf("%s: saved steps %v, want %v", day, got, want)
				}
				for _, legacy := range []bool{false, true} {
					rows, err := db.MetricDataForContract("step_count", day, day+" 23:59:59", "day", "SUM", legacy)
					if err != nil || (want == nil && len(rows) != 0) || (want != nil && (len(rows) != 1 || rows[0].Qty != *want)) {
						t.Fatalf("%s legacy=%v: %v, %v", day, legacy, rows, err)
					}
				}
			}
			first, second := utc, local
			firstName, secondName := "keep", "Health dash - Hourly"
			if localFirst {
				first, second = local, utc
				firstName, secondName = secondName, firstName
			}
			accepted(first, firstName)
			assertDay(first.Date[:10], &first.Qty)
			if got := accepted(second, secondName); !slices.Equal(got, []string{before, after}) {
				t.Fatalf("new alias affected %v", got)
			}
			if got := accepted(second, secondName); !slices.Equal(got, []string{before, after}) {
				t.Fatalf("retry affected %v", got)
			}
			assertDay(before, nil)
			assertDay(after, &local.Qty)
			// A different quantity ends the equivalence; the old losing day becomes visible again.
			corrected := local
			corrected.Qty++
			if got := accepted(corrected, "Health dash - Hourly"); !slices.Equal(got, []string{before, after}) {
				t.Fatalf("correction affected %v", got)
			}
			assertDay(before, &utc.Qty)
			assertDay(after, &corrected.Qty)
			accepted(local, "Health dash - Hourly")
			assertDay(before, nil)
			// Two real local 02:00 hours have distinct instants, even with equal values.
			accepted(point(after+" 02:00:00 +0200", 12), "keep")
			accepted(point(after+" 02:00:00 +0100", 12), "keep")
			sum := 104.0
			assertDay(after, &sum)
			if err := db.RebuildHistoricalCacheDate(t.Context(), before); err != nil {
				t.Fatal(err)
			}
			assertDay(before, nil)
			assertDay(after, &sum)
			db.RemoveAutoExportForRange(after, after)
			assertDay(before, &utc.Qty)
			fold := 24.0
			assertDay(after, &fold)
		})
	}
}

func TestStepImportAliasRepairsDateOutsideCoverage(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	const before = "2026-11-10"
	const after = "2026-11-11"
	importPoint := func(stamp string, qty float64, revision int) {
		t.Helper()
		run, err := db.BeginAppleHealthXMLImport(ImportOptions{SnapshotAt: time.Date(2026, 11, 12, revision, 0, 0, 0, time.UTC)})
		if err != nil {
			t.Fatal(err)
		}
		if err = run.AddPoints([]MetricPoint{{MetricName: "step_count", Units: "count", Source: "Watch", Date: stamp, Qty: qty}}); err != nil {
			t.Fatal(err)
		}
		if _, err = run.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	check := func(day string, want *float64) {
		t.Helper()
		var got *float64
		if err := db.pool.QueryRow(t.Context(), `select (select steps from daily_scores where date=$1)`, day).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if (got == nil) != (want == nil) || (want != nil && *got != *want) {
			t.Fatalf("%s steps: %v, want %v", day, got, want)
		}
	}
	value := 20.0
	importPoint(before+" 23:25:00 +0000", value, 1)
	if err := db.UpsertRecentCache([]string{before}, false); err != nil {
		t.Fatal(err)
	}
	check(before, &value)
	importPoint(after+" 00:25:00 +0100", value, 2)
	check(before, nil)
	if err := db.UpsertRecentCache([]string{after}, false); err != nil {
		t.Fatal(err)
	}
	check(after, &value)
	// A complete subsequent XML day removes the local winner while the UTC
	// copy lies outside the coverage interval of this import.
	importPoint(after+" 01:25:00 +0100", 3, 3)
	check(before, &value)
	if err := db.UpsertRecentCache([]string{after}, false); err != nil {
		t.Fatal(err)
	}
	other := 3.0
	check(after, &other)
}

func TestStepAliasContinuousImportWindow(t *testing.T) {
	var days []string
	start := time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range 3000 {
		days = append(days, start.AddDate(0, 0, i).Format(time.DateOnly))
	}
	edges, nearby := stepAliasWindow(days)
	want := []string{days[0], days[1], days[len(days)-2], days[len(days)-1]}
	if !slices.Equal(edges, want) || len(nearby) != 12 {
		t.Fatalf("boundary %v; %d probe dates", edges, len(nearby))
	}
}

// One unrelated legacy cell freezes its wall hour. Proven duplicate evidence
// can still withdraw the other source from that hour without losing the first.
func TestStepAliasRepairsOnlyProvenCellOfFrozenHour(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	const before = "2026-11-15"
	const after = "2026-11-16"
	const staleHour = before + " 23:00"
	id, err := db.InsertRaw(Record{ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	watch := MetricPoint{MetricName: "step_count", Source: "Apple Watch", Units: "count", Date: before + " 23:00:00 +0000", Qty: 60}
	if err = db.InsertPoints(id, []MetricPoint{watch}); err != nil {
		t.Fatal(err)
	}
	if err = db.UpsertRecentCache([]string{before}, false); err != nil {
		t.Fatal(err)
	}
	_, err = db.pool.Exec(t.Context(), `insert into hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count)
	values ('step_count',$1,'iPhone',100,30,70,2)`, staleHour)
	if err != nil {
		t.Fatal(err)
	}
	watch.Date = after + " 00:00:00 +0100"
	if err = db.InsertPoints(id, []MetricPoint{watch}); err != nil {
		t.Fatal(err)
	}
	for _, refresh := range []func() error{
		func() error { return db.UpsertRecentCache([]string{after}, false) },
		func() error { return db.RebuildHistoricalCacheDate(t.Context(), before) },
		func() error { return db.BackfillAggregates(true) },
	} {
		if err = refresh(); err != nil {
			t.Fatal(err)
		}
		var old, newTotal float64
		var onlyPhone bool
		err = db.pool.QueryRow(t.Context(), `select (select steps from daily_scores where date=$1),(select steps from daily_scores where date=$2),
			(select count(*)=1 and bool_and(source='iPhone' and avg_val=100 and min_val=30 and max_val=70 and sample_count=2)
			 from hourly_metrics where metric_name='step_count' and hour=$3)`, before, after, staleHour).Scan(&old, &newTotal, &onlyPhone)
		if err != nil || old != 100 || newTotal != 60 || !onlyPhone {
			t.Fatalf("old=%g local=%g retained_phone=%v err=%v", old, newTotal, onlyPhone, err)
		}
	}
}
