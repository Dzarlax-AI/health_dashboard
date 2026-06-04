package storage

import (
	"testing"

	"health-receiver/internal/health"
)

func TestBuildReadinessEvidence_DoesNotUsePriorDayAsSameDayRHR(t *testing.T) {
	rhr := 55.0
	latest := dailyScoreRow{
		date: "2026-06-03",
		rhr:  &rhr,
	}

	e := buildReadinessEvidence("2026-06-04", latest, nil)
	if e.RHR.Present {
		t.Fatalf("RHR present = true; prior-day RHR must not be treated as same-day evidence")
	}
	if e.RHR.SourceDate != "" {
		t.Fatalf("RHR source date = %q, want empty for missing same-day evidence", e.RHR.SourceDate)
	}
}

func TestBuildReadinessEvidence_UsesFreshSameDayCounts(t *testing.T) {
	hrv := 80.0
	rhr := 72.0
	sleep := 7.5
	fresh := &dayRow{
		hrv:  &hrv,
		rhr:  &rhr,
		slp:  &sleep,
		hrvN: 4,
		rhrN: 1,
	}

	e := buildReadinessEvidence("2026-06-04", dailyScoreRow{date: "2026-06-04"}, fresh)
	if !e.HRV.Present || e.HRV.SampleCount != 4 {
		t.Fatalf("HRV evidence = present %v samples %d, want present with 4 samples", e.HRV.Present, e.HRV.SampleCount)
	}
	if !e.RHR.Present || e.RHR.SourceDate != "2026-06-04" {
		t.Fatalf("RHR evidence = present %v source %q, want same-day present", e.RHR.Present, e.RHR.SourceDate)
	}
}

func TestBuildReadinessEvidence_HRVSampleThresholdsAreMonotonic(t *testing.T) {
	hrv := 80.0

	provisional := buildReadinessEvidence("2026-06-04", dailyScoreRow{date: "2026-06-04"}, &dayRow{hrv: &hrv, hrvN: health.MinUnalignedHRVSamplesForProvisionalUse})
	if provisional.HRV.Confidence != health.ReadinessConfidenceProvisional {
		t.Fatalf("HRV confidence at provisional threshold = %q", provisional.HRV.Confidence)
	}

	final := buildReadinessEvidence("2026-06-04", dailyScoreRow{date: "2026-06-04"}, &dayRow{hrv: &hrv, hrvN: health.MinSleepWindowHRVSamplesForFullConfidence})
	if final.HRV.Confidence != health.ReadinessConfidenceFinal {
		t.Fatalf("HRV confidence at full threshold = %q", final.HRV.Confidence)
	}
}
