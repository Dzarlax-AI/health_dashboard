package storage

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

const importHRVDay = "2026-07-01"
const importHRVAt = importHRVDay + " 08:00:00 +0200"

func assertImportHRVRepairDates(t *testing.T, db *DB, expected ...string) {
	t.Helper()
	state, err := db.LoadCacheMaintenance(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	rows, err := db.pool.Query(t.Context(), `SELECT key,value FROM settings WHERE key LIKE $1`, hrvRepairKeyPrefix+"%")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := make(map[string]bool, len(expected))
	for _, day := range expected {
		want[day] = true
	}
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			t.Fatal(err)
		}
		day := strings.TrimPrefix(key, hrvRepairKeyPrefix)
		generation, err := strconv.ParseUint(value, 10, 64)
		if !want[day] || err != nil || generation == 0 || state.Dirty[day] != generation {
			t.Fatalf("unexpected import repair entry for %s", day)
		}
		delete(want, day)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 0 {
		t.Fatalf("missing import repair dates: %v", want)
	}
}

func seedImportHRV(t *testing.T, db *DB, extra ...MetricPoint) {
	t.Helper()
	points := append([]MetricPoint{hrvRepairPoint(importHRVAt, "Watch", 60)}, extra...)
	if err := db.InsertPoints(insertTestRawRecord(t, db, "xml-hrv-live-fixture"), points); err != nil {
		t.Fatal(err)
	}
	for _, day := range []string{importHRVDay, "2026-07-02"} {
		if err := db.upsertHourlyAvgForDate(day); err != nil {
			t.Fatal(err)
		}
		if err := db.upsertDailyForDate(day); err != nil {
			t.Fatal(err)
		}
	}
}

func TestAppleHealthXMLImportHRVRepairAuthoritativeSubset(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	seedImportHRV(t, db,
		hrvRepairPoint(importHRVDay+" 09:00:00 +0200", "Watch", 70),
		hrvRepairPoint(importHRVAt, "Other", 80),
		hrvRepairPoint("2026-07-02 08:00:00 +0200", "Watch", 55),
		MetricPoint{MetricName: "heart_rate", Units: "count/min", Date: importHRVAt, Source: "Watch", Qty: 65},
	)
	snapshot := time.Now().Add(time.Minute)
	commit := func() ImportCounters {
		t.Helper()
		session, err := db.BeginAppleHealthXMLImport(ImportOptions{SourceName: "xml-hrv-subset", SnapshotAt: snapshot})
		if err != nil {
			t.Fatal(err)
		}
		if err := session.AddPoints([]MetricPoint{hrvRepairPoint(importHRVAt, "Watch", 60)}); err != nil {
			t.Fatal(err)
		}
		counters, err := session.Commit()
		if err != nil {
			t.Fatal(err)
		}
		return counters
	}
	if got := commit(); got.DeletedPoints != 1 {
		t.Fatalf("deleted points = %d, want only the omitted HRV point", got.DeletedPoints)
	}
	assertImportHRVRepairDates(t, db, importHRVDay)
	assertPointMissing(t, db, "heart_rate_variability", importHRVDay+" 09:00:00 +0200", "Watch")
	assertPointState(t, db, "heart_rate_variability", importHRVAt, "Other", 80, "live")
	assertPointState(t, db, "heart_rate", importHRVAt, "Watch", 65, "live")
	repaired, err := db.rebuildRecentHRVRepair(t.Context(), importHRVDay, liveDailySQL(false))
	if err != nil || !repaired {
		t.Fatalf("consume imported HRV repair: repaired=%v err=%v", repaired, err)
	}
	want, neighbor := 70.0, 55.0
	assertHRVRepairValue(t, db, importHRVDay, &want)
	assertHRVRepairValue(t, db, "2026-07-02", &neighbor)
	assertHRVRepairHours(t, db, importHRVDay, map[string]map[string]float64{"08:00": {"Watch": 60, "Other": 80}})
	assertImportHRVRepairDates(t, db)
	if got := commit(); got.DeletedPoints != 0 {
		t.Fatalf("replay deleted %d points", got.DeletedPoints)
	}
	assertImportHRVRepairDates(t, db)
}

func TestAppleHealthXMLImportHRVRepairIgnoresNonMutatingCoverage(t *testing.T) {
	for _, scenario := range []string{"stale", "unverified", "unrelated", "already-invalid", "last-staged-valid"} {
		t.Run(scenario, func(t *testing.T) {
			db, cleanup := testDB(t)
			defer cleanup()
			seedImportHRV(t, db,
				hrvRepairPoint(importHRVDay+" 09:00:00 +0200", "Watch", 70),
				MetricPoint{MetricName: "heart_rate", Units: "count/min", Date: importHRVAt, Source: "Watch", Qty: 65},
			)
			opts := ImportOptions{SourceName: "xml-hrv-no-repair", SnapshotAt: time.Now().Add(time.Minute)}
			points := []MetricPoint{hrvRepairPoint(importHRVAt, "Watch", 60)}
			switch scenario {
			case "stale":
				opts.SnapshotAt = time.Now().Add(-time.Hour)
				points[0].Qty = 0
			case "unverified":
				opts.UnverifiedCoverage = true
			case "unrelated":
				points = []MetricPoint{{MetricName: "heart_rate", Units: "count/min", Date: importHRVAt, Source: "Watch", Qty: 66}}
			case "already-invalid":
				if _, err := db.pool.Exec(t.Context(), `UPDATE metric_points SET quality='impossible' WHERE metric_name='heart_rate_variability' AND date=$1`, importHRVDay+" 09:00:00 +0200"); err != nil {
					t.Fatal(err)
				}
			case "last-staged-valid":
				opts.UnverifiedCoverage = true
				points = append([]MetricPoint{hrvRepairPoint(importHRVAt, "Watch", 0)}, points...)
			}
			session, err := db.BeginAppleHealthXMLImport(opts)
			if err != nil {
				t.Fatal(err)
			}
			if err := session.AddPoints(points); err != nil {
				t.Fatal(err)
			}
			if _, err := session.Commit(); err != nil {
				t.Fatal(err)
			}
			assertImportHRVRepairDates(t, db)
			if scenario != "already-invalid" {
				assertPointState(t, db, "heart_rate_variability", importHRVDay+" 09:00:00 +0200", "Watch", 70, "live")
			}
		})
	}
}

func TestAppleHealthXMLImportHRVRepairNonpositiveReplacement(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	seedImportHRV(t, db)
	session := beginTestXMLImport(t, db, "xml-hrv-zero-replacement")
	if err := session.AddPoints([]MetricPoint{hrvRepairPoint(importHRVAt, "Watch", 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Commit(); err != nil {
		t.Fatal(err)
	}
	assertImportHRVRepairDates(t, db, importHRVDay)
	repaired, err := db.rebuildRecentHRVRepair(t.Context(), importHRVDay, liveDailySQL(false))
	if err != nil || !repaired {
		t.Fatalf("consume zero-replacement repair: repaired=%v err=%v", repaired, err)
	}
	assertHRVRepairValue(t, db, importHRVDay, nil)
	assertHRVRepairHours(t, db, importHRVDay, nil)
	assertImportHRVRepairDates(t, db)
}

func TestAppleHealthXMLImportHRVRepairRollsBackWithPromote(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	seedImportHRV(t, db)
	before, err := db.LoadCacheMaintenance(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(t.Context(), `CREATE FUNCTION reject_hrv_import_commit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'synthetic import commit failure'; END $$;
		CREATE TRIGGER reject_hrv_import_commit BEFORE UPDATE ON import_runs
		FOR EACH ROW WHEN (NEW.status='committed') EXECUTE FUNCTION reject_hrv_import_commit()`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.pool.Exec(t.Context(), `DROP TRIGGER reject_hrv_import_commit ON import_runs; DROP FUNCTION reject_hrv_import_commit()`); err != nil {
			t.Error(err)
		}
	}()
	session := beginTestXMLImport(t, db, "xml-hrv-rollback")
	if err := session.AddPoints([]MetricPoint{hrvRepairPoint(importHRVAt, "Watch", 0)}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Commit(); err == nil {
		t.Fatal("expected synthetic promote failure")
	}
	assertPointState(t, db, "heart_rate_variability", importHRVAt, "Watch", 60, "live")
	assertImportHRVRepairDates(t, db)
	after, err := db.LoadCacheMaintenance(t.Context())
	if err != nil || before.Generation != after.Generation {
		t.Fatalf("failed promote changed cache generation: %v", err)
	}
}

func TestAppleHealthXMLImportHRVRepairMixedIngestJournalLockOrder(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	if err := db.EnsureCompletedNightSleepTableContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	seedImportHRV(t, db)
	recordID := insertTestRawRecord(t, db, "mixed-night-and-hrv")
	tx, err := db.pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockCacheMaintenanceRow(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	done := make(chan error, 1)
	joined := false
	defer func() {
		_ = tx.Rollback(context.Background())
		if !joined {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("mixed ingest did not stop after releasing the fixture journal")
			}
		}
	}()
	go func() {
		_, err := db.InsertAcceptedPoints(recordID, []MetricPoint{
			{MetricName: "night_sleep_total", Units: "hr", Date: importHRVDay + " 07:00:00 +0200", Source: "Watch", Qty: 7},
			hrvRepairPoint(importHRVAt, "Watch", 70),
		}, nil)
		done <- err
	}()
	// Observe the writer blocked on the journal before asserting that it
	// holds no point-row lock. This avoids a timing-only concurrency test.
	deadline := time.Now().Add(5 * time.Second)
	blocked := false
	for time.Now().Before(deadline) {
		if _, err := tx.Exec(t.Context(), `SELECT pg_stat_clear_snapshot()`); err != nil {
			t.Fatal(err)
		}
		if err := tx.QueryRow(t.Context(), `SELECT EXISTS (
			SELECT FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid()
			AND pg_backend_pid()=ANY(pg_blocking_pids(pid))
			AND wait_event_type='Lock' AND query LIKE 'SELECT value FROM settings WHERE key = %FOR UPDATE'
		)`).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !blocked {
		t.Fatal("mixed ingest did not reach the held cache journal")
	}
	var previous float64
	if err := tx.QueryRow(t.Context(), `SELECT qty FROM metric_points
		WHERE metric_name='heart_rate_variability' AND date=$1 AND source='Watch' FOR UPDATE NOWAIT`, importHRVAt).Scan(&previous); err != nil || previous != 60 {
		t.Fatalf("mixed ingest locked or changed HRV before acquiring its journal: %v", err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		joined = true
		t.Fatal(err)
	}
	joined = true
	assertPointState(t, db, "heart_rate_variability", importHRVAt, "Watch", 70, "live")
	assertImportHRVRepairDates(t, db)
}
