package storage

import (
	"context"
	"testing"
	"time"

	"health-receiver/internal/health"
)

// This is deliberately DB-backed: the safety boundary is the exact join
// between an adapter commitment and its raw night_sleep_total point, not the
// pure B0 calculation alone.
func TestCompletedNightSleepRequiresCommittedRawPointAndFinalizes(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Fatalf("load test timezone: %v", err)
	}
	if err := db.SaveSettings(map[string]string{"timezone": "Europe/Belgrade"}); err != nil {
		t.Fatalf("set tenant timezone: %v", err)
	}
	wakeDate := "2026-09-10"
	metricDate := "2026-09-10 00:00:00 +0200"
	source := "Apple Watch"
	recordID := insertTestRawRecord(t, db, "completed-night-sleep")
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO metric_points (health_record_id,metric_name,units,date,qty,source,quality)
		VALUES ($1,'night_sleep_total','hr',$2,7.25,$3,'ok')`, recordID, metricDate, source); err != nil {
		t.Fatalf("seed night_sleep_total: %v", err)
	}
	start := time.Date(2026, 9, 9, 12, 0, 0, 0, loc)
	end := start.Add(24 * time.Hour)
	if err := db.SaveNightSleepCoverageCommitment(ctx, NightSleepCoverageCommitment{
		WakeDate: wakeDate, MetricDate: metricDate, Source: source, SourceEpoch: "health-sync-ios-v1",
		CaptureCompleteness: health.NightCaptureComplete, CoverageGeneration: "scan-1",
		CoveredIntervalStart: start, CoveredIntervalEnd: end, ObservedAt: end,
		InputHash: "coverage-input-1",
	}); err != nil {
		t.Fatalf("save coverage commitment: %v", err)
	}

	morning := time.Date(2026, 9, 10, 10, 0, 0, 0, loc)
	if err := db.ReconcileCompletedNightSleep(ctx, wakeDate, morning); err != nil {
		t.Fatalf("reconcile morning: %v", err)
	}
	nights, err := db.ListCompletedNightSleep(ctx, wakeDate, wakeDate)
	if err != nil || len(nights) != 1 {
		t.Fatalf("read canonical night: nights=%#v err=%v", nights, err)
	}
	if got := nights[0]; got.DurationHours != 7.25 || got.FinalizationState != health.NightFinalProvisional || got.ClaimEligibility != health.NightClaimIneligible {
		t.Fatalf("morning canonical night = %#v", got)
	}

	// The periodic finalizer must reuse the canonical writer's assessment and
	// eligibility rules for outliers and partial captures as well as the normal night.
	for _, test := range []struct {
		date     string
		hours    float64
		capture  string
		complete bool
	}{
		{date: "2026-09-08", hours: 15, capture: health.NightCaptureComplete, complete: true},
		{date: "2026-09-09", hours: 7, capture: health.NightCapturePartial},
	} {
		wake, _ := time.ParseInLocation(time.DateOnly, test.date, loc)
		observed := time.Date(wake.Year(), wake.Month(), wake.Day(), 11, 0, 0, 0, loc)
		var coveredStart, coveredEnd *time.Time
		generation := ""
		if test.complete {
			end := time.Date(wake.Year(), wake.Month(), wake.Day(), 12, 0, 0, 0, loc)
			start := end.AddDate(0, 0, -1)
			coveredStart, coveredEnd, generation = &start, &end, "complete-"+test.date
		}
		canonical, err := health.CanonicalizeCompletedNight(health.CanonicalNightInput{
			WakeDate: test.date, DurationHours: test.hours, Source: source, SourceEpoch: "health-sync-ios-v1",
			InputHash: "input-" + test.date, CaptureCompleteness: test.capture, CoverageGeneration: generation,
			CoveredIntervalStart: coveredStart, CoveredIntervalEnd: coveredEnd, ObservedAt: observed,
			AlgorithmVersion: CompletedNightSleepAlgorithmVersion,
		}, morning, loc)
		if err != nil {
			t.Fatalf("canonicalize %s: %v", test.date, err)
		}
		if err := db.SaveCompletedNightSleep(ctx, canonical, nil); err != nil {
			t.Fatalf("save %s: %v", test.date, err)
		}
	}
	// Legacy rows may claim complete/plausible without coverage metadata. The
	// historical finalizer trusted those persisted fields, kept the assessment,
	// and preserved an existing finalized_at value.
	legacyDate := "2026-09-07"
	legacyStart := time.Date(2026, 9, 6, 12, 0, 0, 0, loc)
	legacyEnd := legacyStart.AddDate(0, 0, 1)
	legacy, err := health.CanonicalizeCompletedNight(health.CanonicalNightInput{
		WakeDate: legacyDate, DurationHours: 7, Source: source, SourceEpoch: "health-sync-ios-v1",
		InputHash: "legacy-input", CaptureCompleteness: health.NightCaptureComplete,
		CoverageGeneration: "legacy-generation", CoveredIntervalStart: &legacyStart, CoveredIntervalEnd: &legacyEnd,
		ObservedAt: legacyEnd, AlgorithmVersion: CompletedNightSleepAlgorithmVersion,
	}, morning, loc)
	if err != nil {
		t.Fatalf("canonicalize legacy row: %v", err)
	}
	if err := db.SaveCompletedNightSleep(ctx, legacy, nil); err != nil {
		t.Fatalf("save legacy row: %v", err)
	}
	legacyFinalizedAt := time.Date(2026, 9, 7, 18, 5, 0, 0, loc)
	if _, err := db.pool.Exec(ctx, `update completed_night_sleep set duration_hours=15,coverage_generation=null,covered_interval_start=null,covered_interval_end=null,finalized_at=$2 where wake_date=$1`, legacyDate, legacyFinalizedAt); err != nil {
		t.Fatalf("prepare legacy row: %v", err)
	}

	evening := time.Date(2026, 9, 10, 19, 0, 0, 0, loc)
	if err := db.FinalizeCompletedNightSleep(ctx, evening); err != nil {
		t.Fatalf("finalize evening: %v", err)
	}
	nights, err = db.ListCompletedNightSleep(ctx, wakeDate, wakeDate)
	if err != nil || len(nights) != 1 {
		t.Fatalf("read finalized night: nights=%#v err=%v", nights, err)
	}
	if got := nights[0]; got.FinalizationState != health.NightFinalFinal || got.ClaimEligibility != health.NightClaimEligible || got.FinalizedAt == nil || !got.FinalizedAt.Equal(evening) {
		t.Fatalf("finalized canonical night = %#v", got)
	}
	// Re-running the periodic writer must preserve the first finalization instant.
	if err := db.FinalizeCompletedNightSleep(ctx, evening.Add(time.Hour)); err != nil {
		t.Fatalf("repeat finalization: %v", err)
	}
	nights, err = db.ListCompletedNightSleep(ctx, wakeDate, wakeDate)
	if err != nil || len(nights) != 1 || nights[0].FinalizedAt == nil || !nights[0].FinalizedAt.Equal(evening) {
		t.Fatalf("repeat finalization changed timestamp: %#v err=%v", nights, err)
	}
	earlier, err := db.ListCompletedNightSleep(ctx, "2026-09-08", "2026-09-09")
	if err != nil || len(earlier) != 2 {
		t.Fatalf("read finalized outlier/partial nights: %#v %v", earlier, err)
	}
	for _, night := range earlier {
		if night.FinalizationState != health.NightFinalFinal || night.ClaimEligibility != health.NightClaimIneligible || night.FinalizedAt == nil {
			t.Fatalf("invalid finalized non-claim state: %#v", night)
		}
	}
	if earlier[0].DurationAssessment != health.NightDurationOutlier || earlier[1].DurationAssessment != health.NightDurationPlausible {
		t.Fatalf("finalizer changed assessments: %#v", earlier)
	}
	legacyRows, err := db.ListCompletedNightSleep(ctx, legacyDate, legacyDate)
	if err != nil || len(legacyRows) != 1 {
		t.Fatalf("read finalized legacy row: %#v %v", legacyRows, err)
	}
	if got := legacyRows[0]; got.FinalizationState != health.NightFinalFinal || got.ClaimEligibility != health.NightClaimEligible || got.DurationAssessment != health.NightDurationPlausible || got.CaptureCompleteness != health.NightCaptureComplete || got.FinalizedAt == nil || !got.FinalizedAt.Equal(legacyFinalizedAt) {
		t.Fatalf("legacy row behavior changed: %#v", got)
	}
}

func TestNightCoverageIdenticalReplayPreservesOriginalObservation(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := db.SaveSettings(map[string]string{"timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	c := NightSleepCoverageCommitment{WakeDate: "2026-09-10", MetricDate: "2026-09-10 00:00:00 +0000", Source: "Apple Watch", SourceEpoch: "ios", CaptureCompleteness: health.NightCapturePartial, CoverageGeneration: "same-generation", CoveredIntervalStart: start, CoveredIntervalEnd: start.Add(24 * time.Hour), ObservedAt: start.Add(time.Hour), InputHash: "same-input"}
	if err := db.SaveNightSleepCoverageCommitment(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.ObservedAt = c.ObservedAt.Add(time.Hour)
	if err := db.SaveNightSleepCoverageCommitment(ctx, c); err != nil {
		t.Fatal(err)
	}
	var got time.Time
	if err := db.pool.QueryRow(ctx, `SELECT observed_at FROM night_sleep_coverage_commitments WHERE wake_date=$1 AND source=$2`, c.WakeDate, c.Source).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Equal(start.Add(time.Hour)) {
		t.Fatalf("identical replay changed observation: %s", got)
	}
	// Receipt time and opaque generation do not order different inputs: a
	// delayed replay and a legitimate correction remain indistinguishable.
}
