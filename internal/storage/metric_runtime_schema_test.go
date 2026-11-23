package storage

import (
	"os"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestMetricSchemaCoreReaders(t *testing.T) {
	dir := os.Getenv("HEALTH_METRIC_AUDIT_DIR")
	if dir == "" || os.Getenv("SLEEP_REFERENCE_PILOT_MARKER") == "" {
		t.Skip("requires marked synthetic sleep fixture and audit directory")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	now := time.Now().UTC()
	date := now.Format(time.DateOnly)
	goal := health.SleepGoal{EffectiveDate: "2020-01-01", Hours: 8, Version: "synthetic-v1"}
	if err := db.SaveSleepGoal(ctx, goal, now); err != nil {
		t.Fatal(err)
	}
	loadedGoal, err := db.GetEffectiveSleepGoal(ctx, date)
	if err != nil || loadedGoal == nil {
		t.Fatalf("goal: %v", err)
	}
	if err = db.SaveSettings(map[string]string{sleepComparisonSetting: "true"}); err != nil {
		t.Fatal(err)
	}
	night := health.CompletedNightSleep{WakeDate: date, DurationHours: 7.5, Source: "Synthetic Watch", SourceEpoch: "synthetic", AlgorithmVersion: "synthetic", InputHash: "synthetic", CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible, FinalizationState: health.NightFinalFinal, ClaimEligibility: health.NightClaimEligible, ObservedAt: now}
	if err = db.SaveCompletedNightSleep(ctx, night, nil); err != nil {
		t.Fatal(err)
	}
	_, err = db.pool.Exec(ctx, `update completed_night_sleep set diagnostics='{"preferred_sleep_source":"Synthetic Watch","source_selection_reason":"preferred"}' where wake_date=$1`, date)
	if err != nil {
		t.Fatal(err)
	}
	comparison := db.GetSleepDurationComparison(ctx, date)
	if comparison == nil || comparison.SourceSelectionReason == "" {
		t.Fatalf("comparison: %+v", comparison)
	}
	sample := 50.0
	component := readinessComponent("heart_rate_variability", date, date, &sample, 4, "")
	_, err = db.pool.Exec(ctx, `insert into energy_snapshots(ts_bucket,date,bank,drain_delta,restore_delta,formula_version,components,flags) values($1,$2,70,2,1,3,'{}','{}')`, now, date)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := db.GetEnergyHistoryV2(ctx, "UTC", 24)
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("energy snapshots %d: %v", len(snapshots), err)
	}
	if err = db.SaveEnergyBankSnapshot(date, &health.EnergyBank{Capacity: 80, Current: 70, DrainSoFar: 10, ActionVerdict: "rest"}); err != nil {
		t.Fatal(err)
	}
	legacy, err := db.GetEnergyHistory(14)
	if err != nil || len(legacy) != 1 {
		t.Fatalf("legacy snapshots %d: %v", len(legacy), err)
	}
	key := map[string]any{"date": date}
	writeMetricRuntimeCapture(t, dir, "core_readers.json", metricRuntimeCapture{"sleep_goal", "storage.GetEffectiveSleepGoal", loadedGoal, map[string]any{"effective_date": loadedGoal.EffectiveDate, "version": loadedGoal.Version}}, metricRuntimeCapture{"sleep_comparison", "storage.GetSleepDurationComparison", comparison, map[string]any{"wake_date": comparison.WakeDate}}, metricRuntimeCapture{"readiness_component", "storage.readinessComponent", component, map[string]any{"date": date, "metric": component.Metric}}, metricRuntimeCapture{"energy_snapshot", "storage.GetEnergyHistoryV2", snapshots[0], map[string]any{"ts_bucket": snapshots[0].TS}}, metricRuntimeCapture{"legacy_energy_eod", "storage.GetEnergyHistory", legacy[0], key})
}
