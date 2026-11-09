package health

import "testing"

func TestMorningActivityCannotUseCurrentDateMeasurements(t *testing.T) {
	steps, hrv := 4000.0, 45.0
	daily := []DailyHealthMetrics{{Date: "2026-11-08", Steps: &steps}, {Date: "2026-11-09", Steps: &steps, Calories: &steps, Exercise: &steps, HRV: &hrv}}
	evidence := MorningInsightEvidence{Date: "2026-11-09", Daily: daily, NightSleep: &MorningReportSleep{YesterdayActivity: &MorningYesterdayActivity{Date: "2026-11-08", Steps: &steps}}}
	NormalizeMorningInsightEvidence(&evidence, "en")
	if evidence.Daily[1].Steps != nil || evidence.Daily[1].Calories != nil || evidence.Daily[1].Exercise != nil || evidence.Daily[1].HRV == nil || evidence.Daily[0].Steps == nil || daily[1].Steps == nil || evidence.NightSleep.YesterdayActivity.Date != "2026-11-08" {
		t.Fatalf("activity date boundary or source snapshot changed: %+v", evidence)
	}
}

func TestApplyPreliminaryMorningInsightPolicySanitizesSleepAndBuildsScopedOptions(t *testing.T) {
	hours, hrv, sleepValue := 7.5, 51.0, 8.0
	evidence := MorningInsightEvidence{
		Date: "2026-09-30",
		NightSleep: &MorningReportSleep{
			ReportDate: "2026-09-30", Date: "2026-09-30", Hours: &hours,
			BaselineHours: &hours, BaselineNights: 12,
			Capture: NightCapturePartial, Assessment: NightDurationPlausible, Finalization: NightFinalProvisional,
		},
		Daily: []DailyHealthMetrics{{Date: "2026-09-30", HRV: &hrv, Sleep: &sleepValue, Deep: &sleepValue}},
		Sections: []MorningInsightSection{
			{Key: "sleep", Summary: "Do not expose sleep section text."},
			{Key: "activity", Summary: "Activity remained steady.", Details: []BriefingDetail{{Label: "Steps", Note: "within the recent range", Value: "9000"}}},
			{Key: "cardio", Summary: "Cardio markers were consistent."},
			{Key: "recovery", Summary: "Recovery signals were mixed.", Details: []BriefingDetail{{Label: "HRV", Note: "close to its recent range", Value: "51 ms"}}},
		},
	}
	originalDaily := evidence.Daily
	if !ApplyPreliminaryMorningInsightPolicy(&evidence, "en") {
		t.Fatal("current plausible partial sleep was not treated as preliminary")
	}
	if evidence.NightSleep.BaselineHours != nil || evidence.NightSleep.BaselineNights != 0 {
		t.Fatalf("preliminary baseline leaked: %#v", evidence.NightSleep)
	}
	if evidence.Daily[0].Sleep != nil || evidence.Daily[0].Deep != nil || evidence.Daily[0].HRV == nil || originalDaily[0].Sleep == nil {
		t.Fatalf("daily sleep was not removed from a cloned evidence slice: evidence=%#v original=%#v", evidence.Daily, originalDaily)
	}
	if got := evidence.PreliminaryOptions.Activity; len(got) != 3 || got[0].Text != "Activity remained steady." || got[1].Text != "Steps: within the recent range" || got[2].Text != "Cardio markers were consistent." {
		t.Fatalf("activity options = %#v", got)
	}
	if got := evidence.PreliminaryOptions.Recovery; len(got) != 2 || got[0].Text != "Recovery signals were mixed." || got[1].Text != "HRV: close to its recent range" {
		t.Fatalf("recovery options = %#v", got)
	}
	for _, option := range append(evidence.PreliminaryOptions.Activity, evidence.PreliminaryOptions.Recovery...) {
		if option.Text == "Do not expose sleep section text." {
			t.Fatalf("sleep section entered a non-sleep option: %#v", option)
		}
	}
	if !ApplyPreliminaryMorningInsightPolicy(&evidence, "en") || evidence.PreliminaryOptions.Activity[1].ID != "activity_2" {
		t.Fatalf("policy is not idempotent: %#v", evidence.PreliminaryOptions)
	}
}

func TestApplyPreliminaryMorningInsightPolicyUsesLocalizedFallbacksOnlyWhenNeeded(t *testing.T) {
	hours := 6.5
	evidence := MorningInsightEvidence{Date: "2026-09-30", NightSleep: &MorningReportSleep{
		ReportDate: "2026-09-30", Date: "2026-09-30", Hours: &hours,
		Capture: NightCaptureComplete, Assessment: NightDurationPlausible, Finalization: NightFinalProvisional,
	}}
	if !ApplyPreliminaryMorningInsightPolicy(&evidence, "sr") {
		t.Fatal("same-day complete provisional sleep was not eligible")
	}
	if len(evidence.PreliminaryOptions.Activity) != 1 || evidence.PreliminaryOptions.Activity[0].Text != "Za sada ima malo podataka o aktivnosti i kardio-pokazateljima." {
		t.Fatalf("localized activity fallback = %#v", evidence.PreliminaryOptions.Activity)
	}
	if len(evidence.PreliminaryOptions.Recovery) != 1 || evidence.PreliminaryOptions.Recovery[0].Text != "Za sada ima malo podataka o oporavku." {
		t.Fatalf("localized recovery fallback = %#v", evidence.PreliminaryOptions.Recovery)
	}
}

func TestNormalizeMorningInsightEvidenceKeepsOnlyCurrentComparableFinalBaseline(t *testing.T) {
	hours, baseline := 7.5, 8.0
	base := func() MorningInsightEvidence {
		return MorningInsightEvidence{
			Date: "2026-09-30",
			NightSleep: &MorningReportSleep{ReportDate: "2026-09-30", Date: "2026-09-30", Hours: &hours,
				BaselineHours: &baseline, BaselineNights: 7, Capture: NightCaptureComplete,
				Assessment: NightDurationPlausible, Finalization: NightFinalFinal},
			PreliminaryOptions: &PreliminaryMorningInsightOptions{Activity: []MorningInsightOption{{ID: "forged", Text: "untrusted"}}},
		}
	}
	evidence := base()
	if NormalizeMorningInsightEvidence(&evidence, "en") || evidence.PreliminaryOptions != nil || evidence.NightSleep.BaselineHours == nil || evidence.NightSleep.BaselineNights != 7 {
		t.Fatalf("current final baseline should survive and supplied options should be cleared: %#v", evidence)
	}
	insufficient := base()
	insufficient.NightSleep.BaselineNights = 6
	if NormalizeMorningInsightEvidence(&insufficient, "en") || insufficient.NightSleep.BaselineHours != nil || insufficient.NightSleep.BaselineNights != 0 {
		t.Fatalf("insufficient baseline survived normalization: %#v", insufficient.NightSleep)
	}
	stale := base()
	stale.NightSleep.Date = "2026-09-29"
	if NormalizeMorningInsightEvidence(&stale, "en") || stale.NightSleep.BaselineHours != nil || stale.NightSleep.BaselineNights != 0 {
		t.Fatalf("stale-night baseline survived normalization: %#v", stale.NightSleep)
	}
}

func TestPreliminarySleepFiltersSharedContextAndPreservesOtherDomains(t *testing.T) {
	hours := 7.0
	evidence := MorningInsightEvidence{Date: "2026-11-09", NightSleep: &MorningReportSleep{Date: "2026-11-09", Hours: &hours, Capture: NightCapturePartial, Assessment: NightDurationPlausible, Finalization: NightFinalProvisional}, FactContext: &InsightFactContext{Guidance: AIInsightServerView{Meaning: "sleep-derived"}, Facts: []DailyInsightNarrativeFact{{ID: "sleep_total", Domain: "sleep", Statement: "sleep"}, {ID: "readiness_current", Domain: "recovery", Statement: "recovery"}}, DomainStates: []AIInsightDomainState{{Domain: "recovery", DataState: "fresh", Confidence: "final"}}}}
	if !NormalizeMorningInsightEvidence(&evidence, "en") {
		t.Fatal("expected preliminary")
	}
	if len(evidence.FactContext.Facts) != 1 || evidence.FactContext.Facts[0].Domain != "recovery" || evidence.FactContext.Guidance.Meaning != "" {
		t.Fatalf("unfiltered context: %#v", evidence.FactContext)
	}
	if len(evidence.PreliminaryOptions.Recovery) != 0 || len(evidence.PreliminaryOptions.Activity) == 0 {
		t.Fatalf("domain freshness ignored: %#v", evidence.PreliminaryOptions)
	}
}
