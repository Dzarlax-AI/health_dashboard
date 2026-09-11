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

	evening := time.Date(2026, 9, 10, 19, 0, 0, 0, loc)
	if err := db.FinalizeCompletedNightSleep(ctx, evening); err != nil {
		t.Fatalf("finalize evening: %v", err)
	}
	nights, err = db.ListCompletedNightSleep(ctx, wakeDate, wakeDate)
	if err != nil || len(nights) != 1 {
		t.Fatalf("read finalized night: nights=%#v err=%v", nights, err)
	}
	if got := nights[0]; got.FinalizationState != health.NightFinalFinal || got.ClaimEligibility != health.NightClaimEligible || got.FinalizedAt == nil {
		t.Fatalf("finalized canonical night = %#v", got)
	}
}
