package health

import (
	"testing"
	"time"
)

func TestSelectMorningReportSleepPreservesCurrentPartialAndComparableBaseline(t *testing.T) {
	day := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	nights := []CompletedNightSleep{}
	for i := 0; i < 12; i++ {
		nights = append(nights, CompletedNightSleep{WakeDate: day.AddDate(0, 0, -i).Format("2006-01-02"), DurationHours: 8, Source: "watch", SourceEpoch: "epoch", AlgorithmVersion: "v1", CaptureCompleteness: NightCaptureComplete, DurationAssessment: NightDurationPlausible, FinalizationState: NightFinalFinal, ClaimEligibility: NightClaimEligible})
	}
	nights[0].DurationHours = 4
	nights[0].CaptureCompleteness = NightCapturePartial
	nights[0].FinalizationState = NightFinalProvisional
	nights[0].InputHash = "new-night"
	nights[1].SourceEpoch = "old-epoch"
	nights[2].AlgorithmVersion = "old-algorithm"
	nights[3].Source = "other-watch"
	nights[4].CaptureCompleteness = NightCaptureUnknown
	future := nights[5]
	future.WakeDate = "2026-10-01"
	nights = append(nights, future)
	got := SelectMorningReportSleep("2026-09-30", nights)
	if got.Date != "2026-09-30" || got.Hours == nil || *got.Hours != 4 || got.Capture != NightCapturePartial || got.InputHash != "new-night" {
		t.Fatalf("current partial hidden: %#v", got)
	}
	if got.BaselineNights != 7 || got.BaselineHours == nil || *got.BaselineHours != 8 {
		t.Fatalf("comparable baseline: %#v", got)
	}
	nights[5].ClaimEligibility = NightClaimIneligible
	got = SelectMorningReportSleep("2026-09-30", nights)
	if got.BaselineNights != 6 || got.BaselineHours != nil {
		t.Fatalf("insufficient baseline accepted: %#v", got)
	}
}

func TestSelectMorningReportSleepMissingAndStale(t *testing.T) {
	got := SelectMorningReportSleep("2026-09-30", nil)
	if got.Hours != nil || got.Date != "" || got.ReportDate != "2026-09-30" {
		t.Fatalf("missing replaced: %#v", got)
	}
	got = SelectMorningReportSleep("2026-09-30", []CompletedNightSleep{{WakeDate: "2026-09-29", DurationHours: 7}})
	if got.Date != "2026-09-29" || got.ReportDate != "2026-09-30" {
		t.Fatalf("stale night redated: %#v", got)
	}
}
