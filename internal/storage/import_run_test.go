package storage

import (
	"errors"
	"testing"
	"time"
)

func TestAppleHealthXMLImportFreshnessOrder(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	recordID := insertTestRawRecord(t, db, "live-before")
	if err := db.InsertPoints(recordID, []MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        60,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("seed live point: %v", err)
	}

	xml := beginTestXMLImport(t, db, "xml-1")
	if err := xml.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        61,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage xml point: %v", err)
	}
	if _, err := xml.Commit(); err != nil {
		t.Fatalf("commit xml: %v", err)
	}
	assertPointState(t, db, "heart_rate", "2026-07-01 10:00:00 +0200", "Apple Watch", 61, appleHealthXMLOrigin)

	recordID = insertTestRawRecord(t, db, "live-after")
	if err := db.InsertPoints(recordID, []MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        62,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("write later live point: %v", err)
	}
	assertPointState(t, db, "heart_rate", "2026-07-01 10:00:00 +0200", "Apple Watch", 62, "live")

	laterXML := beginTestXMLImport(t, db, "xml-2")
	if err := laterXML.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        63,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage later xml point: %v", err)
	}
	if _, err := laterXML.Commit(); err != nil {
		t.Fatalf("commit later xml: %v", err)
	}
	assertPointState(t, db, "heart_rate", "2026-07-01 10:00:00 +0200", "Apple Watch", 63, appleHealthXMLOrigin)
}

func TestAppleHealthXMLImportDeletesOnlyCoveredStaleRows(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	recordID := insertTestRawRecord(t, db, "old-live")
	if err := db.InsertPoints(recordID, []MetricPoint{
		{MetricName: "heart_rate", Units: "count/min", Date: "2026-07-01 10:00:00 +0200", Qty: 60, Source: "Apple Watch"},
		{MetricName: "heart_rate", Units: "count/min", Date: "2026-07-01 11:00:00 +0200", Qty: 70, Source: "Apple Watch"},
		{MetricName: "step_count", Units: "count", Date: "2026-07-01 10:00:00 +0200", Qty: 1000, Source: "iPhone"},
	}); err != nil {
		t.Fatalf("seed live points: %v", err)
	}

	xml := beginTestXMLImport(t, db, "xml-covered")
	if err := xml.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        61,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage xml point: %v", err)
	}
	counters, err := xml.Commit()
	if err != nil {
		t.Fatalf("commit xml: %v", err)
	}
	if counters.DeletedPoints != 1 {
		t.Fatalf("DeletedPoints = %d, want 1", counters.DeletedPoints)
	}
	assertPointState(t, db, "heart_rate", "2026-07-01 10:00:00 +0200", "Apple Watch", 61, appleHealthXMLOrigin)
	assertPointMissing(t, db, "heart_rate", "2026-07-01 11:00:00 +0200", "Apple Watch")
	assertPointState(t, db, "step_count", "2026-07-01 10:00:00 +0200", "iPhone", 1000, "live")
}

func TestAppleHealthXMLImportUsesLastStagedDuplicatePoint(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	xml := beginTestXMLImport(t, db, "xml-duplicates")
	if err := xml.AddPoints([]MetricPoint{
		{MetricName: "heart_rate", Units: "count/min", Date: "2026-07-01 10:00:00 +0200", Qty: 60, Source: "Apple Watch"},
		{MetricName: "heart_rate", Units: "count/min", Date: "2026-07-01 10:00:00 +0200", Qty: 61, Source: "Apple Watch"},
	}); err != nil {
		t.Fatalf("stage duplicate xml points: %v", err)
	}
	counters, err := xml.Commit()
	if err != nil {
		t.Fatalf("commit xml: %v", err)
	}
	if counters.InsertedPoints != 1 || counters.SkippedPoints != 1 {
		t.Fatalf("inserted/skipped = %d/%d, want 1/1", counters.InsertedPoints, counters.SkippedPoints)
	}
	assertPointState(t, db, "heart_rate", "2026-07-01 10:00:00 +0200", "Apple Watch", 61, appleHealthXMLOrigin)
}

func TestAppleHealthXMLImportDoesNotOverwriteLiveRowsNewerThanSnapshot(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	xml, err := db.BeginAppleHealthXMLImport(ImportOptions{
		SourceName: "xml-old-snapshot",
		SnapshotAt: time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("begin xml import: %v", err)
	}
	if err := xml.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        61,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage xml point: %v", err)
	}

	recordID := insertTestRawRecord(t, db, "live-during-import")
	if err := db.InsertPoints(recordID, []MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        62,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("write newer live point: %v", err)
	}

	counters, err := xml.Commit()
	if err != nil {
		t.Fatalf("commit xml: %v", err)
	}
	if counters.UpdatedPoints != 0 || counters.SkippedPoints != 1 {
		t.Fatalf("counters updated/skipped = %d/%d, want 0/1", counters.UpdatedPoints, counters.SkippedPoints)
	}
	assertPointState(t, db, "heart_rate", "2026-07-01 10:00:00 +0200", "Apple Watch", 62, "live")
}

func TestAppleHealthXMLImportDoesNotOverwriteLiveConflictUpdateNewerThanSnapshot(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	firstXML := beginTestXMLImport(t, db, "xml-existing")
	if err := firstXML.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        61,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage first xml point: %v", err)
	}
	if _, err := firstXML.Commit(); err != nil {
		t.Fatalf("commit first xml: %v", err)
	}

	oldSnapshotXML, err := db.BeginAppleHealthXMLImport(ImportOptions{
		SourceName: "xml-old-snapshot",
		SnapshotAt: time.Now().Add(-1 * time.Hour),
	})
	if err != nil {
		t.Fatalf("begin old snapshot xml import: %v", err)
	}
	if err := oldSnapshotXML.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        60,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage old snapshot xml point: %v", err)
	}

	recordID := insertTestRawRecord(t, db, "live-conflict-update")
	if err := db.InsertPoints(recordID, []MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        62,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("write live conflict update: %v", err)
	}

	counters, err := oldSnapshotXML.Commit()
	if err != nil {
		t.Fatalf("commit old snapshot xml: %v", err)
	}
	if counters.UpdatedPoints != 0 || counters.SkippedPoints != 1 {
		t.Fatalf("counters updated/skipped = %d/%d, want 0/1", counters.UpdatedPoints, counters.SkippedPoints)
	}
	assertPointState(t, db, "heart_rate", "2026-07-01 10:00:00 +0200", "Apple Watch", 62, "live")
}

func TestAppleHealthXMLImportAbortLeavesRowsUntouched(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	xml := beginTestXMLImport(t, db, "xml-abort")
	if err := xml.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        61,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage xml point: %v", err)
	}
	xml.Abort(errors.New("parse failed"))

	ctx, cancel := queryCtx()
	defer cancel()
	var points int
	if err := db.pool.QueryRow(ctx, `SELECT COUNT(*) FROM metric_points`).Scan(&points); err != nil {
		t.Fatalf("count points: %v", err)
	}
	if points != 0 {
		t.Fatalf("metric_points count = %d, want 0", points)
	}
	var status, msg string
	if err := db.pool.QueryRow(ctx, `SELECT status, error FROM import_runs WHERE id = $1`, xml.runID).Scan(&status, &msg); err != nil {
		t.Fatalf("read import run: %v", err)
	}
	if status != "failed" || msg != "parse failed" {
		t.Fatalf("import status/error = %q/%q, want failed/parse failed", status, msg)
	}
	assertStageCounts(t, db, xml.runID, 0, 0)
	var parsedPoints int64
	if err := db.pool.QueryRow(ctx, `SELECT parsed_points FROM import_runs WHERE id = $1`, xml.runID).Scan(&parsedPoints); err != nil {
		t.Fatalf("read parsed_points: %v", err)
	}
	if parsedPoints != 1 {
		t.Fatalf("parsed_points after abort = %d, want 1", parsedPoints)
	}
}

func TestAppleHealthXMLImportPersistentStageRowsVisibleAndCleaned(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	xml := beginTestXMLImport(t, db, "xml-persistent-stage")
	start := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	end := start.Add(30 * time.Minute)
	if err := xml.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        61,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage xml point: %v", err)
	}
	if err := xml.AddWorkouts([]Workout{{
		ExternalID:  "applexml:stagevisible",
		Name:        "Strength Training",
		StartTime:   start,
		EndTime:     end,
		DurationSec: end.Sub(start).Seconds(),
	}}); err != nil {
		t.Fatalf("stage xml workout: %v", err)
	}
	assertStageCounts(t, db, xml.runID, 1, 1)

	if _, err := xml.Commit(); err != nil {
		t.Fatalf("commit xml: %v", err)
	}
	assertStageCounts(t, db, xml.runID, 0, 0)
	assertPointState(t, db, "heart_rate", "2026-07-01 10:00:00 +0200", "Apple Watch", 61, appleHealthXMLOrigin)
}

func TestAppleHealthXMLImportCrossRunStageIsolationForStaleDelete(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	recordID := insertTestRawRecord(t, db, "old-live")
	if err := db.InsertPoints(recordID, []MetricPoint{
		{MetricName: "heart_rate", Units: "count/min", Date: "2026-07-01 10:00:00 +0200", Qty: 60, Source: "Apple Watch"},
		{MetricName: "heart_rate", Units: "count/min", Date: "2026-07-01 11:00:00 +0200", Qty: 70, Source: "Apple Watch"},
	}); err != nil {
		t.Fatalf("seed live points: %v", err)
	}

	first := beginTestXMLImport(t, db, "xml-first")
	if err := first.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        61,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage first point: %v", err)
	}

	second := beginTestXMLImport(t, db, "xml-second")
	if err := second.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 11:00:00 +0200",
		Qty:        71,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage second point: %v", err)
	}

	counters, err := first.Commit()
	if err != nil {
		t.Fatalf("commit first: %v", err)
	}
	if counters.DeletedPoints != 1 {
		t.Fatalf("first DeletedPoints = %d, want 1", counters.DeletedPoints)
	}
	assertPointMissing(t, db, "heart_rate", "2026-07-01 11:00:00 +0200", "Apple Watch")
	assertStageCounts(t, db, first.runID, 0, 0)
	assertStageCounts(t, db, second.runID, 1, 0)
}

func TestAppleHealthXMLImportCleanupAbandonedStagesPreservesRecentRunning(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	recent := beginTestXMLImport(t, db, "xml-recent-running")
	if err := recent.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        61,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage recent point: %v", err)
	}

	old := beginTestXMLImport(t, db, "xml-old-running")
	if err := old.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-02 10:00:00 +0200",
		Qty:        62,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage old point: %v", err)
	}

	ctx, cancel := queryCtx()
	defer cancel()
	if _, err := db.pool.Exec(ctx, `UPDATE import_runs SET started_at = NOW() - INTERVAL '48 hours', heartbeat_at = NOW() - INTERVAL '48 hours' WHERE id = $1`, old.runID); err != nil {
		t.Fatalf("age old import: %v", err)
	}

	if err := db.CleanupAbandonedImportStages(24 * time.Hour); err != nil {
		t.Fatalf("cleanup abandoned stages: %v", err)
	}
	assertStageCounts(t, db, recent.runID, 1, 0)
	assertStageCounts(t, db, old.runID, 0, 0)

	var oldStatus string
	if err := db.pool.QueryRow(ctx, `SELECT status FROM import_runs WHERE id = $1`, old.runID).Scan(&oldStatus); err != nil {
		t.Fatalf("read old status: %v", err)
	}
	if oldStatus != "failed" {
		t.Fatalf("old status = %q, want failed", oldStatus)
	}
}

func TestBeginAppleHealthXMLImportEnsuresPersistentStageSchema(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	ctx, cancel := queryCtx()
	defer cancel()
	if _, err := db.pool.Exec(ctx, `DROP TABLE IF EXISTS import_stage_workouts, import_stage_points`); err != nil {
		t.Fatalf("drop stage tables: %v", err)
	}
	defer db.EnsureIndexes()

	xml := beginTestXMLImport(t, db, "xml-ensure-stage-schema")
	if err := xml.AddPoints([]MetricPoint{{
		MetricName: "heart_rate",
		Units:      "count/min",
		Date:       "2026-07-01 10:00:00 +0200",
		Qty:        61,
		Source:     "Apple Watch",
	}}); err != nil {
		t.Fatalf("stage point after schema ensure: %v", err)
	}
	assertStageCounts(t, db, xml.runID, 1, 0)

	if _, err := xml.Commit(); err != nil {
		t.Fatalf("commit xml: %v", err)
	}
}

func TestAppleHealthXMLImportReplacesSyntheticWorkoutDuplicate(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	start := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	end := start.Add(45 * time.Minute)
	first := beginTestXMLImport(t, db, "workouts-1")
	if err := first.AddWorkouts([]Workout{{
		ExternalID:  "applexml:oldhash",
		Name:        "Outdoor Run",
		StartTime:   start,
		EndTime:     end,
		DurationSec: end.Sub(start).Seconds(),
		DistanceKm:  importFloatPtr(8.0),
	}}); err != nil {
		t.Fatalf("stage first workout: %v", err)
	}
	if _, err := first.Commit(); err != nil {
		t.Fatalf("commit first workout: %v", err)
	}

	second := beginTestXMLImport(t, db, "workouts-2")
	if err := second.AddWorkouts([]Workout{{
		ExternalID:  "applexml:newhash",
		Name:        "Outdoor Run",
		StartTime:   start,
		EndTime:     end,
		DurationSec: end.Sub(start).Seconds(),
		DistanceKm:  importFloatPtr(8.2),
	}}); err != nil {
		t.Fatalf("stage second workout: %v", err)
	}
	counters, err := second.Commit()
	if err != nil {
		t.Fatalf("commit second workout: %v", err)
	}
	if counters.DeletedWorkouts != 1 {
		t.Fatalf("DeletedWorkouts = %d, want 1", counters.DeletedWorkouts)
	}

	ctx, cancel := queryCtx()
	defer cancel()
	var count int
	var externalID string
	var distance float64
	if err := db.pool.QueryRow(ctx, `
		SELECT COUNT(*), MAX(external_id), MAX(distance_km)
		  FROM workouts
		 WHERE name = 'Outdoor Run'
		   AND start_time = $1
		   AND end_time = $2`, start, end).Scan(&count, &externalID, &distance); err != nil {
		t.Fatalf("read workouts: %v", err)
	}
	if count != 1 || externalID != "applexml:newhash" || distance != 8.2 {
		t.Fatalf("workout state = count %d id %q distance %.1f, want 1/newhash/8.2", count, externalID, distance)
	}
}

func TestAppleHealthXMLImportDoesNotOverwriteWorkoutFromNewerSnapshot(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	start := time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC)
	newer, err := db.BeginAppleHealthXMLImport(ImportOptions{SourceName: "newer", SnapshotAt: time.Date(2026, 7, 5, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	workout := Workout{ExternalID: "workout-stable-id", Name: "Outdoor Run", StartTime: start, EndTime: start.Add(time.Hour), DurationSec: 3600, DistanceKm: importFloatPtr(10)}
	if err = newer.AddWorkouts([]Workout{workout}); err != nil {
		t.Fatal(err)
	}
	if _, err = newer.Commit(); err != nil {
		t.Fatal(err)
	}
	older, err := db.BeginAppleHealthXMLImport(ImportOptions{SourceName: "older", SnapshotAt: time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatal(err)
	}
	workout.DistanceKm = importFloatPtr(5)
	if err = older.AddWorkouts([]Workout{workout}); err != nil {
		t.Fatal(err)
	}
	if _, err = older.Commit(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := queryCtx()
	defer cancel()
	var distance float64
	if err = db.pool.QueryRow(ctx, `SELECT distance_km FROM workouts WHERE external_id=$1`, workout.ExternalID).Scan(&distance); err != nil {
		t.Fatal(err)
	}
	if distance != 10 {
		t.Fatalf("older snapshot replaced workout distance: %v", distance)
	}
}

func TestHealthRecordProcessingReplayState(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	id, err := db.InsertRaw(Record{Payload: `{"data":{}}`, PendingProcessing: true, ProcessingKind: "sum"})
	if err != nil {
		t.Fatal(err)
	}
	pending, err := db.PendingHealthRecords(10)
	if err != nil || len(pending) != 1 || pending[0].ID != id || pending[0].ProcessingKind != "sum" {
		t.Fatalf("pending records = %+v, %v", pending, err)
	}
	if err = db.SetHealthRecordProcessing(id, "complete", nil); err != nil {
		t.Fatal(err)
	}
	pending, err = db.PendingHealthRecords(10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after completion = %+v, %v", pending, err)
	}
}

func TestNotificationDeliveryAtMostOnceAndFailedRetry(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx, cancel := queryCtx()
	defer cancel()
	token, reserved, err := db.ReserveNotificationDelivery(ctx, "report:morning:2026-07-12")
	if err != nil || !reserved {
		t.Fatalf("first reserve = %v, %v", reserved, err)
	}
	if _, reserved, err = db.ReserveNotificationDelivery(ctx, "report:morning:2026-07-12"); err != nil || reserved {
		t.Fatalf("duplicate reserve = %v, %v", reserved, err)
	}
	if err = db.CompleteNotificationDelivery(ctx, "report:morning:2026-07-12", token, "failed", "provider_rejected"); err != nil {
		t.Fatal(err)
	}
	if _, reserved, err = db.ReserveNotificationDelivery(ctx, "report:morning:2026-07-12"); err != nil || !reserved {
		t.Fatalf("definitive failure retry = %v, %v", reserved, err)
	}
}

func beginTestXMLImport(t *testing.T, db *DB, source string) *ImportSession {
	t.Helper()
	session, err := db.BeginAppleHealthXMLImport(ImportOptions{
		SourceName: source,
		SnapshotAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("begin xml import: %v", err)
	}
	return session
}

func insertTestRawRecord(t *testing.T, db *DB, name string) int64 {
	t.Helper()
	id, err := db.InsertRaw(Record{
		AutomationName: name,
		ContentType:    "test",
		Payload:        "{}",
	})
	if err != nil {
		t.Fatalf("insert raw record: %v", err)
	}
	return id
}

func assertPointState(t *testing.T, db *DB, metric, date, source string, wantQty float64, wantOrigin string) {
	t.Helper()
	ctx, cancel := queryCtx()
	defer cancel()
	var qty float64
	var origin string
	var importRunID *int64
	if err := db.pool.QueryRow(ctx, `
		SELECT qty, origin, import_run_id
		  FROM metric_points
		 WHERE metric_name = $1 AND date = $2 AND source = $3`,
		metric, date, source).Scan(&qty, &origin, &importRunID); err != nil {
		t.Fatalf("read point %s/%s/%s: %v", metric, date, source, err)
	}
	if qty != wantQty || origin != wantOrigin {
		t.Fatalf("point %s/%s/%s = qty %.1f origin %q, want %.1f/%q", metric, date, source, qty, origin, wantQty, wantOrigin)
	}
	if wantOrigin == appleHealthXMLOrigin && importRunID == nil {
		t.Fatalf("xml-origin point has nil import_run_id")
	}
	if wantOrigin == "live" && importRunID != nil {
		t.Fatalf("live-origin point import_run_id = %d, want nil", *importRunID)
	}
}

func assertPointMissing(t *testing.T, db *DB, metric, date, source string) {
	t.Helper()
	ctx, cancel := queryCtx()
	defer cancel()
	var count int
	if err := db.pool.QueryRow(ctx, `
		SELECT COUNT(*)
		  FROM metric_points
		 WHERE metric_name = $1 AND date = $2 AND source = $3`,
		metric, date, source).Scan(&count); err != nil {
		t.Fatalf("count point %s/%s/%s: %v", metric, date, source, err)
	}
	if count != 0 {
		t.Fatalf("point %s/%s/%s count = %d, want 0", metric, date, source, count)
	}
}

func assertStageCounts(t *testing.T, db *DB, runID int64, wantPoints, wantWorkouts int) {
	t.Helper()
	ctx, cancel := queryCtx()
	defer cancel()
	var points, workouts int
	if err := db.pool.QueryRow(ctx, `SELECT COUNT(*) FROM import_stage_points WHERE import_run_id = $1`, runID).Scan(&points); err != nil {
		t.Fatalf("count staged points for run %d: %v", runID, err)
	}
	if err := db.pool.QueryRow(ctx, `SELECT COUNT(*) FROM import_stage_workouts WHERE import_run_id = $1`, runID).Scan(&workouts); err != nil {
		t.Fatalf("count staged workouts for run %d: %v", runID, err)
	}
	if points != wantPoints || workouts != wantWorkouts {
		t.Fatalf("stage counts for run %d = points %d workouts %d, want %d/%d", runID, points, workouts, wantPoints, wantWorkouts)
	}
}

func importFloatPtr(v float64) *float64 {
	return &v
}
