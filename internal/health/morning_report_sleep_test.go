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
	if got.BaselineNights != 0 || got.BaselineHours != nil {
		t.Fatalf("preliminary night exposed a baseline: %#v", got)
	}
	nights[0].CaptureCompleteness = NightCaptureComplete
	nights[0].FinalizationState = NightFinalFinal
	nights[0].ClaimEligibility = NightClaimEligible
	got = SelectMorningReportSleep("2026-09-30", nights)
	if got.BaselineNights != 7 || got.BaselineHours == nil || *got.BaselineHours != 8 {
		t.Fatalf("comparable final baseline: %#v", got)
	}
	nights[5].ClaimEligibility = NightClaimIneligible
	got = SelectMorningReportSleep("2026-09-30", nights)
	if got.BaselineNights != 6 || got.BaselineHours != nil {
		t.Fatalf("insufficient baseline accepted: %#v", got)
	}
}

func TestPreliminaryMorningSleepExplanationEligibility(t *testing.T) {
	hours := 7.5
	base := MorningReportSleep{ReportDate: "2026-09-30", Date: "2026-09-30", Hours: &hours, Assessment: NightDurationPlausible, Capture: NightCapturePartial, Finalization: NightFinalProvisional}
	cases := []struct {
		name  string
		sleep MorningReportSleep
		want  bool
	}{
		{"partial current", base, true},
		{"partial finalized", func() MorningReportSleep { v := base; v.Finalization = NightFinalFinal; return v }(), true},
		{"complete provisional", func() MorningReportSleep { v := base; v.Capture = NightCaptureComplete; return v }(), true},
		{"final complete", func() MorningReportSleep {
			v := base
			v.Capture = NightCaptureComplete
			v.Finalization = NightFinalFinal
			return v
		}(), false},
		{"unknown assessment", func() MorningReportSleep { v := base; v.Assessment = NightDurationUnknown; return v }(), false},
		{"outlier assessment", func() MorningReportSleep { v := base; v.Assessment = NightDurationOutlier; return v }(), false},
		{"stale date", func() MorningReportSleep { v := base; v.Date = "2026-09-29"; return v }(), false},
		{"missing duration", func() MorningReportSleep { v := base; v.Hours = nil; return v }(), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := PreliminaryMorningSleepExplanationEligible(&tc.sleep, "2026-09-30"); got != tc.want {
				t.Fatalf("eligible = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestMorningSleepExplanationLocalizesOnlyApprovedChoices(t *testing.T) {
	for _, lang := range []string{"en", "ru", "sr"} {
		for _, choice := range []string{MorningSleepChoiceRecordedDuration, MorningSleepChoiceAwaitingCompletion, MorningSleepChoiceAwaitingFinalization} {
			if text, ok := MorningSleepExplanation(choice, lang); !ok || text == "" {
				t.Fatalf("choice %q lang %q: text=%q ok=%v", choice, lang, text, ok)
			}
		}
	}
	if _, ok := MorningSleepExplanation("sleep_quality_was_good", "en"); ok {
		t.Fatal("unapproved choice accepted")
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
