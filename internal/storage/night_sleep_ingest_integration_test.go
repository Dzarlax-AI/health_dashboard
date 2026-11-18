package storage

import (
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestAcceptedNightIngestProtectsFullVersionAndPoint(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	day := "2026-09-20"
	noon, _ := time.Parse(time.RFC3339, day+"T12:00:00Z")
	first := MetricPoint{MetricName: "night_sleep_total", Date: day + " 07:00:00 +0000", Source: "Apple Watch", Units: "hr", Qty: 9.05}
	c := NightSleepCoverageCommitment{
		WakeDate: day, MetricDate: first.Date, Source: first.Source, SourceEpoch: "fixture",
		CaptureCompleteness: health.NightCaptureComplete, CoverageGeneration: "fixture-a", InputHash: "a",
		CoveredIntervalStart: noon.AddDate(0, 0, -1), CoveredIntervalEnd: noon,
	}
	apply := func(point MetricPoint, commitments []NightSleepCoverageCommitment, expected bool) int64 {
		t.Helper()
		id := insertTestRawRecord(t, db, "night-sync")
		result, err := db.InsertAcceptedPoints(id, []MetricPoint{point}, commitments)
		if err != nil || (len(result) == 1) != expected {
			t.Fatalf("apply point: applied=%v want %v err=%v", result, expected, err)
		}
		return id
	}
	check := func(point MetricPoint, id int64) {
		t.Helper()
		nights, err := db.ListCompletedNightSleep(ctx, day, day)
		if err != nil || len(nights) != 1 || float32(nights[0].DurationHours) != float32(point.Qty) ||
			nights[0].CaptureCompleteness != health.NightCaptureComplete ||
			nights[0].FinalizationState != health.NightFinalFinal {
			t.Fatalf("night did not retain complete value: %+v %v", nights, err)
		}
		var count int
		var actualID int64
		if err := db.pool.QueryRow(ctx, `SELECT count(*), max(health_record_id) FROM metric_points
			WHERE metric_name='night_sleep_total' AND source=$1 AND SUBSTRING(date,1,10)=$2`, point.Source, day).Scan(&count, &actualID); err != nil || count != 1 || actualID != id {
			t.Fatalf("authoritative point count=%d stored record=%d expected record=%d err=%v", count, actualID, id, err)
		}
	}
	firstID := apply(first, []NightSleepCoverageCommitment{c}, true)
	partial, cut := c, first
	partial.CaptureCompleteness, partial.InputHash = health.NightCapturePartial, "partial"
	cut.Qty = 1.13
	apply(cut, []NightSleepCoverageCommitment{partial}, false)
	apply(cut, nil, false)
	apply(first, []NightSleepCoverageCommitment{partial}, false)
	other, otherCoverage := cut, partial
	other.Source, otherCoverage.Source = "Apple Watch 2", "Apple Watch 2"
	apply(other, []NightSleepCoverageCommitment{otherCoverage}, false)
	check(first, firstID)
	// A pre-fix row has a coverage-only hash and no version ledger. The
	// first guarded correction still records the active full fingerprint.
	if _, err := db.pool.Exec(ctx, `UPDATE night_sleep_coverage_commitments SET input_hash=$1 WHERE wake_date=$2`, c.InputHash, day); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `UPDATE completed_night_sleep SET diagnostics=diagnostics-$1 WHERE wake_date=$2`, nightIngestDiagnosticsKey, day); err != nil {
		t.Fatal(err)
	}

	correction, next := first, c
	correction.Qty, correction.Date = 7.5, day+" 06:30:00 +0000"
	next.MetricDate, next.InputHash = correction.Date, "b"
	next.CoverageGeneration = "fixture-b"
	nextID := apply(correction, []NightSleepCoverageCommitment{next}, true)
	apply(first, []NightSleepCoverageCommitment{c}, false)
	if applied, err := db.InsertAcceptedPoints(firstID, []MetricPoint{first}, []NightSleepCoverageCommitment{c}); err != nil || len(applied) != 0 {
		t.Fatalf("out-of-order retry used the old record: %+v %v", applied, err)
	}
	if err := db.ReconcileCompletedNightSleep(ctx, day, time.Now()); err != nil {
		t.Fatal(err)
	}
	check(correction, nextID)
	cold := db.rawMetricsFromPoints(day)
	if _, err := db.pool.Exec(ctx, `INSERT INTO daily_scores(date,sleep_total) VALUES ($1,7.5)`, day); err != nil {
		t.Fatal(err)
	}
	warm := db.rawMetricsFromDailyScoresAt(day, false)
	for _, raw := range []*health.RawMetrics{cold, warm} {
		if raw == nil || len(raw.NightSleep) != 1 || raw.NightSleep[0] != correction.Qty {
			t.Fatalf("moved wake key was counted more than once: %+v", raw)
		}
	}

	// One packet with conflicting quantities/coverage cannot replace the
	// version. An actual subsequent correction with the same coverage can.
	newPoint := correction
	newPoint.Qty = 7.75
	id := insertTestRawRecord(t, db, "night-sync")
	if applied, err := db.InsertAcceptedPoints(id, []MetricPoint{newPoint, correction}, []NightSleepCoverageCommitment{next}); err != nil || len(applied) != 0 {
		t.Fatalf("ambiguous pair changed state: %+v %v", applied, err)
	}
	check(correction, nextID)
	thirdID := apply(newPoint, []NightSleepCoverageCommitment{next}, true)
	apply(correction, []NightSleepCoverageCommitment{next}, false)
	check(newPoint, thirdID)
}

func TestAcceptedNightIngestUsesTenantWakeDate(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SaveSettings(map[string]string{"timezone": loc.String()}); err != nil {
		t.Fatal(err)
	}
	apply := func(day, stamp, source string, wantAccepted bool) {
		t.Helper()
		wake, err := time.ParseInLocation(time.DateOnly, day, loc)
		if err != nil {
			t.Fatal(err)
		}
		noon := time.Date(wake.Year(), wake.Month(), wake.Day(), 12, 0, 0, 0, loc)
		point := MetricPoint{MetricName: "night_sleep_total", Date: stamp, Source: source, Units: "hr", Qty: 7}
		coverage := NightSleepCoverageCommitment{
			WakeDate: day, MetricDate: stamp, Source: source, SourceEpoch: "fixture",
			CaptureCompleteness: health.NightCaptureComplete, CoverageGeneration: day, InputHash: day,
			CoveredIntervalStart: noon.AddDate(0, 0, -1), CoveredIntervalEnd: noon,
		}
		points, err := db.InsertAcceptedPoints(insertTestRawRecord(t, db, "night-sync"), []MetricPoint{point}, []NightSleepCoverageCommitment{coverage})
		if err != nil || (len(points) == 1) != wantAccepted {
			t.Fatalf("wake date %s, writer time %s: accepted %d, want accepted=%v, err=%v", day, stamp, len(points), wantAccepted, err)
		}
	}
	ctx := t.Context()
	// One writer date, two tenant-local dates, both valid nights.
	apply("2026-09-20", "2026-09-20 07:00:00 +0000", "fixture", true)
	apply("2026-09-21", "2026-09-20 22:30:00 +0000", "fixture", true)
	nights, err := db.ListCompletedNightSleep(ctx, "2026-09-20", "2026-09-21")
	if err != nil || len(nights) != 2 {
		t.Fatalf("expected both canonical nights, got %d: %v", len(nights), err)
	}
	dirty, err := db.CaptureCacheDirty(ctx, []string{"2026-09-20", "2026-09-21"})
	if err != nil || len(dirty) != 2 {
		t.Fatalf("expected both dates in cache recovery, got %v: %v", dirty, err)
	}

	// Different writer dates, one local date. An unclaimed point on that
	// night still blocks a second point from the same source.
	prior := MetricPoint{MetricName: "night_sleep_total", Date: "2026-09-22 23:30:00 +0000", Source: "legacy", Units: "hr", Qty: 8}
	if err := db.InsertPoints(insertTestRawRecord(t, db, "legacy"), []MetricPoint{prior}); err != nil {
		t.Fatal(err)
	}
	apply("2026-09-23", "2026-09-23T07:00:00+02:00", prior.Source, false)
}

func TestAcceptedNightIngestRollbackAndConflictResolution(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	day := "2026-09-21"
	noon, _ := time.Parse(time.RFC3339, day+"T12:00:00Z")
	point := MetricPoint{MetricName: "night_sleep_total", Date: day + " 07:00:00 +0000", Source: "Apple Watch A", Units: "hr", Qty: 7}
	coverage := NightSleepCoverageCommitment{WakeDate: day, MetricDate: point.Date, Source: point.Source, SourceEpoch: "fixture",
		CaptureCompleteness: health.NightCaptureComplete, CoverageGeneration: "first", InputHash: "first",
		CoveredIntervalStart: noon.AddDate(0, 0, -1), CoveredIntervalEnd: noon}
	record, err := db.InsertRaw(Record{Payload: "{}", PendingProcessing: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.pool.Exec(ctx, `CREATE FUNCTION reject_nightly_repair() RETURNS trigger LANGUAGE plpgsql AS $$
	BEGIN RAISE EXCEPTION 'fixture rejects canonical write'; END $$;
	CREATE TRIGGER reject_nightly_repair BEFORE INSERT OR UPDATE ON completed_night_sleep
	FOR EACH ROW EXECUTE FUNCTION reject_nightly_repair()`); err != nil {
		t.Fatal(err)
	}
	defer db.pool.Exec(ctx, `DROP TRIGGER IF EXISTS reject_nightly_repair ON completed_night_sleep; DROP FUNCTION IF EXISTS reject_nightly_repair()`)
	if _, err := db.InsertAcceptedPoints(record, []MetricPoint{point}, []NightSleepCoverageCommitment{coverage}); err == nil {
		t.Fatal("expected canonical failure")
	}
	var status string
	var points, commitments int
	if err = db.pool.QueryRow(ctx, `SELECT processing_status,
		(SELECT count(*) FROM metric_points WHERE health_record_id=$1),
		(SELECT count(*) FROM night_sleep_coverage_commitments WHERE wake_date=$2)
		FROM health_records WHERE id=$1`, record, day).Scan(&status, &points, &commitments); err != nil || status != "pending" || points != 0 || commitments != 0 {
		t.Fatalf("transaction leaked state: status=%s points=%d commitments=%d err=%v", status, points, commitments, err)
	}
	if _, err = db.pool.Exec(ctx, `DROP TRIGGER reject_nightly_repair ON completed_night_sleep`); err != nil {
		t.Fatal(err)
	}
	if result, err := db.InsertAcceptedPoints(record, []MetricPoint{point}, []NightSleepCoverageCommitment{coverage}); err != nil || len(result) != 1 {
		t.Fatalf("retry: %+v %v", result, err)
	}
	other, otherCoverage := point, coverage
	other.Source, otherCoverage.Source, otherCoverage.InputHash, other.Qty = "Apple Watch B", "Apple Watch B", "second", 8
	otherID := insertTestRawRecord(t, db, "night-sync")
	if result, err := db.InsertAcceptedPoints(otherID, []MetricPoint{other}, []NightSleepCoverageCommitment{otherCoverage}); err != nil || len(result) != 1 {
		t.Fatalf("full competing source: %+v %v", result, err)
	}
	assertStatus := func(want string) {
		t.Helper()
		rows, err := db.ListCompletedNightSleep(ctx, day, day)
		if err != nil || len(rows) != 1 || rows[0].CaptureCompleteness != want {
			t.Fatalf("want %s, got %+v %v", want, rows, err)
		}
	}
	assertStatus(health.NightCaptureUnknown)
	if _, err = db.pool.Exec(ctx, `UPDATE metric_points SET quality='impossible' WHERE health_record_id=$1`, otherID); err != nil {
		t.Fatal(err)
	}
	if err = db.ReconcileCompletedNightSleep(ctx, day, time.Now()); err != nil {
		t.Fatal(err)
	}
	assertStatus(health.NightCaptureComplete)
}

func TestAcceptedNightIngestConcurrentDstAndPartialReplay(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.SaveSettings(map[string]string{"timezone": loc.String()}); err != nil {
		t.Fatal(err)
	}
	noon := time.Date(2026, 10, 25, 12, 0, 0, 0, loc)
	day := noon.Format(time.DateOnly)
	first := MetricPoint{MetricName: "night_sleep_total", Date: day + " 07:00:00 +0100", Source: "Apple Watch", Units: "hr", Qty: 7}
	full := NightSleepCoverageCommitment{
		WakeDate: day, MetricDate: first.Date, Source: first.Source, SourceEpoch: "fixture",
		CaptureCompleteness: health.NightCaptureComplete, CoverageGeneration: "full", InputHash: "full",
		CoveredIntervalStart: noon.AddDate(0, 0, -1), CoveredIntervalEnd: noon,
	}
	cropped, partial := first, full
	cropped.Qty = 1
	partial.CaptureCompleteness, partial.InputHash = health.NightCapturePartial, "partial"
	fullID := insertTestRawRecord(t, db, "night-sync")
	partialID := insertTestRawRecord(t, db, "night-sync")

	// If the newer partial packet runs first, the older accepted full packet
	// still upgrades it. A partial replay after that upgrade is ignored.
	if _, err = db.InsertAcceptedPoints(partialID, []MetricPoint{cropped}, []NightSleepCoverageCommitment{partial}); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, payload := range []struct {
		id       int64
		point    MetricPoint
		coverage NightSleepCoverageCommitment
	}{{fullID, first, full}, {partialID, cropped, partial}} {
		go func() {
			<-start
			_, err := db.InsertAcceptedPoints(payload.id, []MetricPoint{payload.point}, []NightSleepCoverageCommitment{payload.coverage})
			results <- err
		}()
	}
	close(start)
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if err = db.FinalizeCompletedNightSleep(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	nights, err := db.ListCompletedNightSleep(ctx, day, day)
	if err != nil || len(nights) != 1 || nights[0].DurationHours != first.Qty || !nights[0].IsDefinitiveClaimEligible() {
		t.Fatalf("concurrent update did not protect the full DST night: %+v %v", nights, err)
	}
}
