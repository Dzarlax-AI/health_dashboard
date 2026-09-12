package health

import (
	"fmt"
	"testing"
	"time"
)

func TestCanonicalizeCompletedNightRequiresCoverageForComplete(t *testing.T) {
	loc := time.FixedZone("test", 0)
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, loc)
	_, err := CanonicalizeCompletedNight(CanonicalNightInput{
		WakeDate: "2026-09-10", DurationHours: 7.2, Source: "Watch", SourceEpoch: "initial",
		InputHash: "hash", CaptureCompleteness: NightCaptureComplete, ObservedAt: now, AlgorithmVersion: "v1",
	}, now, loc)
	if err == nil {
		t.Fatal("complete record without coverage commitment was accepted")
	}
}

func TestCanonicalizeCompletedNightKeepsOutlierSeparateFromCapture(t *testing.T) {
	loc := time.FixedZone("test", 0)
	start := time.Date(2026, 9, 9, 22, 0, 0, 0, loc)
	end := time.Date(2026, 9, 10, 8, 0, 0, 0, loc)
	now := time.Date(2026, 9, 10, 19, 0, 0, 0, loc)
	got, err := CanonicalizeCompletedNight(CanonicalNightInput{
		WakeDate: "2026-09-10", DurationHours: 2.5, Source: "Watch", SourceEpoch: "initial",
		InputHash: "hash", CaptureCompleteness: NightCaptureComplete, CoverageGeneration: "sync-1",
		CoveredIntervalStart: &start, CoveredIntervalEnd: &end, ObservedAt: now, AlgorithmVersion: "v1",
	}, now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if got.CaptureCompleteness != NightCaptureComplete || got.DurationAssessment != NightDurationOutlier || got.FinalizationState != NightFinalFinal || got.ClaimEligibility != NightClaimIneligible {
		t.Fatalf("canonical night = %#v", got)
	}
}

func TestEvaluateRecentSleepBelowReferenceUsesFourNightsAndSevenDayCadence(t *testing.T) {
	loc := time.FixedZone("test", 0)
	date := time.Date(2026, 9, 10, 19, 0, 0, 0, loc)
	records := make([]CompletedNightSleep, 0, 100)
	for offset := -93; offset <= -4; offset++ {
		records = append(records, finalNight(date.AddDate(0, 0, offset), 8, "hash-"+fmt.Sprint(offset)))
	}
	for offset := -3; offset <= 0; offset++ {
		duration := 8.0
		if offset != -1 {
			duration = 7.5
		}
		records = append(records, finalNight(date.AddDate(0, 0, offset), duration, "current-"+fmt.Sprint(offset)))
	}
	got := EvaluateRecentSleepBelowReference(records, "2026-09-10", date, loc)
	if got.State != RecentSleepClaimTrue || got.CurrentShortDays != 3 || got.ReferenceHours != 8 || !got.ActionEvent || got.EvidenceDigest == "" {
		t.Fatalf("claim = %#v", got)
	}

	records = append(records,
		finalNight(date.AddDate(0, 0, -6), 7.4, "prior-short-6"),
		finalNight(date.AddDate(0, 0, -5), 7.4, "prior-short-5"),
		finalNight(date.AddDate(0, 0, -4), 7.4, "prior-short-4"),
	)
	got = EvaluateRecentSleepBelowReference(records, "2026-09-10", date, loc)
	if got.State != RecentSleepClaimTrue || got.ActionEvent {
		t.Fatalf("prior true must suppress action, got %#v", got)
	}
}

func TestRecentSleepEvidenceDigestChangesWithCanonicalInput(t *testing.T) {
	loc := time.FixedZone("test", 0)
	now := time.Date(2026, 9, 10, 19, 0, 0, 0, loc)
	records := make([]CompletedNightSleep, 0, 94)
	for offset := -93; offset <= -4; offset++ {
		records = append(records, finalNight(now.AddDate(0, 0, offset), 8, fmt.Sprintf("baseline-%d", offset)))
	}
	for offset := -3; offset <= 0; offset++ {
		records = append(records, finalNight(now.AddDate(0, 0, offset), 7.4, fmt.Sprintf("current-%d", offset)))
	}
	first := EvaluateRecentSleepBelowReference(records, "2026-09-10", now, loc)
	records[len(records)-1].InputHash = "revised-current-night"
	second := EvaluateRecentSleepBelowReference(records, "2026-09-10", now, loc)
	if first.EvidenceDigest == second.EvidenceDigest {
		t.Fatal("canonical input revision must invalidate the sleep evidence digest")
	}
}

func TestEvaluateRecentSleepBelowReferenceDoesNotTreatUnknownCadenceAsBlocker(t *testing.T) {
	loc := time.FixedZone("test", 0)
	now := time.Date(2026, 9, 10, 19, 0, 0, 0, loc)
	records := make([]CompletedNightSleep, 0, 94)
	for offset := -93; offset <= -4; offset++ {
		records = append(records, finalNight(now.AddDate(0, 0, offset), 8, fmt.Sprintf("baseline-%d", offset)))
	}
	for offset := -3; offset <= 0; offset++ {
		duration := 8.0
		if offset != -1 {
			duration = 7.4
		}
		records = append(records, finalNight(now.AddDate(0, 0, offset), duration, fmt.Sprintf("current-%d", offset)))
	}
	got := EvaluateRecentSleepBelowReference(records, "2026-09-10", now, loc)
	if got.State != RecentSleepClaimTrue || !got.ActionEvent {
		t.Fatalf("unknown earlier cadence date blocked action: %#v", got)
	}
}

func TestEvaluateRecentSleepBelowReferenceDoesNotCreateHistoricalAction(t *testing.T) {
	loc := time.FixedZone("test", 0)
	wakeDate := time.Date(2026, 9, 9, 0, 0, 0, 0, loc)
	now := time.Date(2026, 9, 10, 19, 0, 0, 0, loc)
	records := make([]CompletedNightSleep, 0, 94)
	for offset := -93; offset <= -4; offset++ {
		records = append(records, finalNight(wakeDate.AddDate(0, 0, offset), 8, fmt.Sprintf("baseline-%d", offset)))
	}
	for offset := -3; offset <= 0; offset++ {
		records = append(records, finalNight(wakeDate.AddDate(0, 0, offset), 7.4, fmt.Sprintf("current-%d", offset)))
	}
	got := EvaluateRecentSleepBelowReference(records, wakeDate.Format("2006-01-02"), now, loc)
	if got.State != RecentSleepClaimTrue || got.ActionEvent {
		t.Fatalf("historical evaluation must be a claim, not an action: %#v", got)
	}
}

func TestEvaluateRecentSleepBelowReferenceKeepsMorningObservationProvisional(t *testing.T) {
	loc := time.FixedZone("test", 0)
	now := time.Date(2026, 9, 10, 8, 0, 0, 0, loc)
	start := now.Add(-9 * time.Hour)
	end := now
	current, err := CanonicalizeCompletedNight(CanonicalNightInput{
		WakeDate: "2026-09-10", DurationHours: 7.4, Source: "Watch", SourceEpoch: "initial", InputHash: "today",
		CaptureCompleteness: NightCaptureComplete, CoverageGeneration: "sync-1", CoveredIntervalStart: &start,
		CoveredIntervalEnd: &end, ObservedAt: now, AlgorithmVersion: "v1",
	}, now, loc)
	if err != nil {
		t.Fatal(err)
	}
	records := []CompletedNightSleep{
		finalNight(now.AddDate(0, 0, -3), 8, "prior-3"),
		finalNight(now.AddDate(0, 0, -2), 8, "prior-2"),
		finalNight(now.AddDate(0, 0, -1), 8, "prior-1"),
		current,
	}
	got := EvaluateRecentSleepBelowReference(records, "2026-09-10", now, loc)
	if got.State != RecentSleepClaimProvisional || got.ActionEvent {
		t.Fatalf("morning claim = %#v", got)
	}
}

func TestRecentSleepAvailabilityReportExplainsCanonicalStatesWithoutMeasurements(t *testing.T) {
	loc := time.FixedZone("test", 0)
	now := time.Date(2026, 9, 10, 19, 0, 0, 0, loc)
	records := make([]CompletedNightSleep, 0, 100)
	for offset := -93; offset <= -4; offset++ {
		records = append(records, finalNight(now.AddDate(0, 0, offset), 8, fmt.Sprintf("baseline-%d", offset)))
	}
	for offset := -3; offset <= 0; offset++ {
		duration := 8.0
		if offset != -1 {
			duration = 7.4
		}
		records = append(records, finalNight(now.AddDate(0, 0, offset), duration, fmt.Sprintf("current-%d", offset)))
	}

	report, err := BuildRecentSleepAvailabilityReport(records, "2026-09-10", 1, now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != RecentSleepAvailabilityReportVersion || report.EvaluationDays != 1 || report.ClaimStates[RecentSleepClaimTrue] != 1 || report.ActionEvents != 1 {
		t.Fatalf("availability report = %#v", report)
	}
	if len(report.UnknownReasons) != 0 || len(report.TrueWithoutActionReasons) != 0 {
		t.Fatalf("unexpected report explanations = %#v", report)
	}

	// A missing complete commitment in one of the current four nights must be
	// distinguishable from an insufficient personal reference.
	records[len(records)-1].CaptureCompleteness = NightCapturePartial
	records[len(records)-1].ClaimEligibility = NightClaimIneligible
	report, err = BuildRecentSleepAvailabilityReport(records, "2026-09-10", 1, now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if report.ClaimStates[RecentSleepClaimUnknown] != 1 || report.UnknownReasons["current_four_night_no_complete_coverage"] != 1 {
		t.Fatalf("coverage report = %#v", report)
	}
}

func TestRecentSleepAvailabilityReportExplainsSevenDayActionCadence(t *testing.T) {
	loc := time.FixedZone("test", 0)
	now := time.Date(2026, 9, 10, 19, 0, 0, 0, loc)
	records := make([]CompletedNightSleep, 0, 100)
	for offset := -100; offset <= -4; offset++ {
		records = append(records, finalNight(now.AddDate(0, 0, offset), 8, fmt.Sprintf("baseline-%d", offset)))
	}
	for offset := -6; offset <= 0; offset++ {
		duration := 8.0
		if offset == -6 || offset == -5 || offset == -4 || offset == -3 || offset == -2 || offset == 0 {
			duration = 7.4
		}
		records = append(records, finalNight(now.AddDate(0, 0, offset), duration, fmt.Sprintf("current-%d", offset)))
	}

	report, err := BuildRecentSleepAvailabilityReport(records, "2026-09-10", 1, now, loc)
	if err != nil {
		t.Fatal(err)
	}
	if report.ClaimStates[RecentSleepClaimTrue] != 1 || report.ActionEvents != 0 || report.TrueWithoutActionReasons["recent_final_true"] != 1 {
		t.Fatalf("cadence report = %#v", report)
	}
}

func TestRecentSleepAvailabilityReportRejectsFutureThroughDate(t *testing.T) {
	loc := time.FixedZone("test", 0)
	asOf := time.Date(2026, 9, 10, 9, 0, 0, 0, loc)
	if _, err := BuildRecentSleepAvailabilityReport(nil, "2026-09-11", 1, asOf, loc); err == nil {
		t.Fatal("future availability report date accepted")
	}
}

func finalNight(date time.Time, duration float64, hash string) CompletedNightSleep {
	finalized := time.Date(date.Year(), date.Month(), date.Day(), 18, 0, 0, 0, date.Location())
	return CompletedNightSleep{
		WakeDate: date.Format("2006-01-02"), DurationHours: duration, Source: "Watch", SourceEpoch: "initial", InputHash: hash,
		CaptureCompleteness: NightCaptureComplete, DurationAssessment: NightDurationPlausible, FinalizationState: NightFinalFinal,
		ClaimEligibility: NightClaimEligible, FinalizedAt: &finalized, AlgorithmVersion: "v1",
	}
}
