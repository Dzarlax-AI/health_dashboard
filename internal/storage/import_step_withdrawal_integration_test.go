package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// Exercise the physical edit paths through their public entry points. The two
// Watch samples occupy different seconds of one minute, whose contribution is
// the largest valid quantity, while the much larger phone total ranks second.
func TestAppleHealthXMLImportStepWithdrawalAndAdminRemoval(t *testing.T) {
	const day = "2026-07-01"
	step := func(clock, source string, qty float64) MetricPoint {
		return MetricPoint{MetricName: "step_count", Date: day + " " + clock + " +0200", Source: source, Units: "count", Qty: qty}
	}
	for _, scenario := range []string{"covered", "nonpositive", "stale", "unverified", "last_staged_valid", "protected_v2", "admin", "historical_quality", "rollback"} {
		t.Run(scenario, func(t *testing.T) {
			db, cleanup := testDB(t)
			defer cleanup()
			ctx := t.Context()
			high := step("08:05:01", "Apple Watch", 25)
			low := step("08:05:02", "Apple Watch", 15)
			phone := step("08:40:00", "iPhone", 100)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			highRecord := insertTestRawRecord(t, db, "Health dash - Hourly")
			if scenario == "protected_v2" {
				high.Date = day + " 08:00:00 +0200"
				var err error
				highRecord, err = db.InsertRaw(Record{AutomationName: "test-v2", ContentType: "application/json; metrics-contract=2", Payload: "{}"})
				must(err)
			}
			must(db.InsertPoints(highRecord, []MetricPoint{high}))
			must(db.InsertPoints(insertTestRawRecord(t, db, "other"), []MetricPoint{low, phone}))
			must(db.upsertHourlySumForDate(day))
			must(db.upsertDailyForDate(day))
			check := func(wantWatch, wantDay int) {
				t.Helper()
				var watch, other, daily int
				must(db.pool.QueryRow(ctx, `select coalesce(max(avg_val) filter(where source='Apple Watch'),0),
					max(avg_val) filter(where source='iPhone') from hourly_metrics where metric_name='step_count' and hour=$1`, day+" 08:00").Scan(&watch, &other))
				must(db.pool.QueryRow(ctx, `select steps from daily_scores where date=$1`, day).Scan(&daily))
				if watch != wantWatch || other != 100 || daily != wantDay {
					t.Fatalf("Watch %d, phone %d, day %d; want %d,100,%d", watch, other, daily, wantWatch, wantDay)
				}
			}
			if scenario == "protected_v2" {
				check(40, 25)
			} else {
				check(25, 25)
			}
			if scenario == "admin" || scenario == "historical_quality" {
				// A second hour is covered only by the phone. Revoking a Watch
				// cell must retain that contribution in the same transaction.
				must(db.InsertPoints(insertTestRawRecord(t, db, "phone hour"), []MetricPoint{step("09:01:00", "iPhone", 20)}))
				must(db.upsertHourlySumForDate(day))
				must(db.upsertDailyForDate(day))
				check(25, 45)
				if scenario == "historical_quality" {
					_, err := db.pool.Exec(ctx, `update metric_points set quality='invalid' where metric_name='step_count' and source='Apple Watch'`)
					must(err)
					// A standalone historical hourly publication must already expose
					// the surviving hours before a subsequent daily writer runs.
					must(db.buildHourlyMetric("step_count", "SUM", true))
					check(0, 120)
					must(db.buildDailyMetricCol("steps", "step_count", true))
					check(0, 120)
					return
				}
				const oldClock = "2000-01-01 00:00:00+00"
				_, err := db.pool.Exec(ctx, `update daily_scores set computed_at=$1 where date=$2`, oldClock, day)
				must(err)
				db.RemoveAutoExportForRange(day, day)
				check(15, 35)
				var correctedClock string
				must(db.pool.QueryRow(ctx, `select computed_at from daily_scores where date=$1`, day).Scan(&correctedClock))
				if correctedClock == oldClock {
					t.Fatal("corrected steps retained the old row clock")
				}
				db.RemoveAutoExportForRange(day, day)
				check(15, 35)
				return
			}
			opts := ImportOptions{SourceName: "synthetic step withdrawal", SnapshotAt: time.Now().Add(time.Hour)}
			staged := []MetricPoint{low}
			want := 15
			if scenario == "nonpositive" || scenario == "rollback" {
				staged = append(staged, step("08:05:01", "Apple Watch", 0))
			}
			if scenario == "protected_v2" {
				opts.CatalogNormalized = true
				withdraw := high
				withdraw.Qty = 0
				staged = append(staged, withdraw)
				want = 40 // The wall-hour source cache still contains both positive samples.
			}
			if scenario == "last_staged_valid" {
				zero := high
				zero.Qty = 0
				staged = append(staged, zero, high)
				want = 25
			}
			if scenario == "unverified" {
				opts.UnverifiedCoverage = true
				want = 25
			}
			if scenario == "stale" {
				opts.SnapshotAt = time.Now().Add(-time.Hour)
				staged = append(staged, step("08:05:01", "Apple Watch", 0))
				want = 25
			}
			if scenario == "rollback" {
				_, err := db.pool.Exec(ctx, `create function reject_step_import() returns trigger language plpgsql as $$
					begin raise exception 'synthetic step rollback'; end $$;
					create trigger reject_step_import before update on import_runs for each row
					when (new.status='committed') execute function reject_step_import()`)
				must(err)
				defer func() {
					_, err := db.pool.Exec(ctx, `drop trigger reject_step_import on import_runs; drop function reject_step_import()`)
					must(err)
				}()
			}
			commit := func(points []MetricPoint) error {
				t.Helper()
				s, err := db.BeginAppleHealthXMLImport(opts)
				must(err)
				must(s.AddPoints(points))
				_, err = s.Commit()
				return err
			}
			err := commit(staged)
			if scenario == "rollback" {
				if err == nil {
					t.Fatal("expected synthetic rollback")
				}
				check(25, 25)
				return
			}
			must(err)
			wantDay := want
			if scenario == "last_staged_valid" {
				wantDay = 40 // Both surviving XML samples contribute to the physical hour.
			}
			if scenario == "protected_v2" {
				wantDay = 25 // The v2 total supersedes the same device's legacy samples.
			}
			check(want, wantDay)
			must(commit(staged))
			check(want, wantDay)
			if scenario == "covered" || scenario == "nonpositive" {
				// The last Watch contribution disappears. The phone is retained.
				must(commit([]MetricPoint{step("08:05:02", "Apple Watch", 0)}))
				check(0, 100)
				must(commit([]MetricPoint{step("08:40:00", "iPhone", 0)}))
				var empty bool
				must(db.pool.QueryRow(ctx, `select steps is null from daily_scores where date=$1`, day).Scan(&empty))
				if !empty {
					t.Fatal("day with all step observations withdrawn retained a value")
				}
			}
		})
	}
}

// Hold an uncommitted correction on one connection and start the normal cache
// writers on a separate handle. Observe their PostgreSQL lock wait before
// releasing the old visible data, then verify the completed result.
func TestStepWithdrawalFencesConcurrentPublication(t *testing.T) {
	const day = "2026-07-01"
	for _, writer := range []string{"foreground", "historical", "legacy_hour", "legacy_day", "dashboard"} {
		t.Run(writer, func(t *testing.T) {
			db, cleanup := testDB(t)
			defer cleanup()
			ctx := t.Context()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			point := MetricPoint{MetricName: "step_count", Date: day + " 08:05:00 +0000", Source: "Watch", Qty: 25}
			must(db.InsertPoints(insertTestRawRecord(t, db, "synthetic"), []MetricPoint{point}))
			_, err := db.rebuildRecentAggregateDate(ctx, day, liveDailySQL(false))
			must(err)
			tx, err := db.pool.Begin(ctx)
			must(err)
			defer tx.Rollback(context.Background())
			_, err = lockCacheMaintenanceRow(ctx, tx)
			must(err)
			_, err = tx.Exec(ctx, `delete from metric_points where metric_name='step_count' and date=$1`, point.Date)
			must(err)
			must(repairWithdrawnSteps(ctx, tx, json.RawMessage(`[["2026-07-01 08:05","Watch"]]`), nil))

			second := &DB{pool: db.pool}
			done := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				switch writer {
				case "foreground":
					done <- second.UpsertRecentCache([]string{day}, false)
				case "historical":
					done <- second.RebuildHistoricalCacheDate(ctx, day)
				case "legacy_hour":
					done <- second.buildHourlyMetric("step_count", "SUM", true)
				case "legacy_day":
					done <- second.buildDailyMetricCol("steps", "step_count", true)
				case "dashboard":
					done <- second.refreshDashboardSnapshot(ctx)
				}
			}()
			defer func() {
				_ = tx.Rollback(context.Background())
				select {
				case <-finished:
				case <-time.After(8 * time.Second):
					t.Error("writer failed to finish")
				}
			}()
			waitForCacheJournalWriter(t, tx)
			must(tx.Commit(ctx))
			must(<-done)
			var stale bool
			must(db.pool.QueryRow(ctx, `select exists(select from hourly_metrics where metric_name='step_count')
				or exists(select from daily_scores where date=$1 and steps is not null)
				or exists(select from dashboard_cache_snapshots
					where payload->'cards' @> '[{"metric":"step_count"}]'::jsonb)`, day).Scan(&stale))
			if stale {
				t.Fatal("withdrawn step value reappeared after publication")
			}
		})
	}
}

func TestPhysicalStepWithdrawalRepairsAcceptedMutations(t *testing.T) {
	const day = "2026-07-18"
	for _, scenario := range []string{"live", "admin_zero", "import_zero", "import_archive_delete", "import_archive_replace"} {
		t.Run(scenario, func(t *testing.T) {
			db, cleanup := testDB(t)
			defer cleanup()
			ctx := t.Context()
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			point := MetricPoint{MetricName: "step_count", Units: "count", Source: "Watch", Date: day + " 08:00:00 +0200"}
			v2, err := db.InsertRaw(Record{AutomationName: "Health dash - Hourly", ContentType: "application/json; metrics-contract=2", Payload: "{}"})
			must(err)
			archive := scenario == "import_archive_delete" || scenario == "import_archive_replace"
			if archive {
				_, err := db.pool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,units,date,source,qty,origin,source_snapshot_at)
					VALUES($1,$2,'count',$3,'Watch',35,'apple_health_xml',now())`, v2, archiveSamplePrefix+"step_count", point.Date)
				must(err)
			} else {
				if scenario == "live" {
					point.Qty = 35
				}
				must(db.InsertPoints(v2, []MetricPoint{point}))
			}
			initial := 0
			if archive || scenario == "live" {
				initial = 35
				// An unrelated cache-only hour must survive repair of the selected hour.
				_, err := db.pool.Exec(ctx, `INSERT INTO hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count)
					VALUES('step_count',$1,'iPhone',40,40,40,1)`, day+" 19:00")
				must(err)
				initial += 40
			}
			_, err = db.pool.Exec(ctx, "INSERT INTO daily_scores(date,steps) SELECT day,source_total FROM ("+
				physicalStepTotalsSQL("substring(p.date,1,10)=$1", "hourly_metrics", "substring(hour,1,10)=$1")+") totals", day)
			must(err)
			check := func(want int) {
				t.Helper()
				var got int
				must(db.pool.QueryRow(ctx, "SELECT coalesce(steps,-1) FROM daily_scores WHERE date=$1", day).Scan(&got))
				if got != want {
					t.Fatalf("steps = %d, want %d (-1 denotes missing)", got, want)
				}
			}
			check(initial)
			switch scenario {
			case "live":
				// A stale replacement must not touch the day before a fresh correction arrives.
				old, err := db.InsertRaw(Record{ContentType: "application/json; metrics-contract=2", Payload: "{}"})
				must(err)
				_, err = db.pool.Exec(ctx, "UPDATE health_records SET received_at='1999-01-01' WHERE id=$1", old)
				must(err)
				point.Qty = 0
				must(db.InsertPoints(old, []MetricPoint{point}))
				check(75)
				must(db.InsertPoints(v2, []MetricPoint{point}))
				check(40)
			case "admin_zero":
				db.RemoveAutoExportForRange(day, day)
				check(-1)
			default:
				options := ImportOptions{SourceName: "synthetic withdrawal", SnapshotAt: time.Now().Add(time.Hour), CatalogNormalized: archive}
				s, err := db.BeginAppleHealthXMLImport(options)
				must(err)
				if scenario != "import_archive_replace" {
					point.Date = day + " 09:05:00 +0200"
				}
				must(s.AddPoints([]MetricPoint{point}))
				_, err = s.Commit()
				must(err)
				if archive {
					check(40)
				} else {
					check(-1)
				}
			}
		})
	}
}

func TestStepPositiveCorrectionRequiresMatchingPreImage(t *testing.T) {
	for _, route := range []string{"live", "XML"} {
		for _, complete := range []bool{true, false} {
			db, cleanup := testDB(t)
			t.Run(fmt.Sprintf("%s_complete_%t", route, complete), func(t *testing.T) {
				defer cleanup()
				ctx := t.Context()
				must := func(err error) {
					t.Helper()
					if err != nil {
						t.Fatal(err)
					}
				}
				const date = "2026-07-21"
				const hour = date + " 08:00"
				point := MetricPoint{MetricName: "step_count", Source: "Watch", Units: "count", Date: hour + ":00 +0200", Qty: 5}
				record := insertTestRawRecord(t, db, "synthetic live")
				must(db.InsertPoints(record, []MetricPoint{point}))
				initial := 80
				if complete {
					initial = 5
				}
				_, err := db.pool.Exec(ctx, `INSERT INTO hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count)
				VALUES('step_count',$1,'Watch',$2,$2,$2,1)`, hour, initial)
				must(err)
				_, err = db.pool.Exec(ctx, `INSERT INTO daily_scores(date,steps) VALUES($1,$2)`, date, initial)
				must(err)
				check := func(want int) {
					t.Helper()
					var day, cached int
					must(db.pool.QueryRow(ctx, `SELECT d.steps,h.avg_val FROM daily_scores d JOIN hourly_metrics h ON d.date=substring(h.hour,1,10)
					WHERE d.date=$1 AND h.metric_name='step_count' AND h.source='Watch'`, date).Scan(&day, &cached))
					if day != want || cached != want {
						t.Fatalf("complete=%t: stored %d, cached %d, want %d", complete, day, cached, want)
					}
				}
				apply := func(p MetricPoint) {
					t.Helper()
					if route == "live" {
						must(db.InsertPoints(record, []MetricPoint{p}))
						return
					}
					session, err := db.BeginAppleHealthXMLImport(ImportOptions{SourceName: "positive correction", SnapshotAt: time.Now().Add(time.Hour), UnverifiedCoverage: true})
					must(err)
					must(session.AddPoints([]MetricPoint{p}))
					_, err = session.Commit()
					must(err)
				}
				point.Qty = 9
				apply(point)
				want := 80
				if complete {
					want = 9
				}
				check(want)
				point.Date = date + " 08:30:00 +0200"
				point.Qty = 11
				apply(point)
				if complete {
					want = 20
				}
				check(want)
				must(db.UpsertRecentCache([]string{date}, false))
				check(want)
			})
		}
	}
}
