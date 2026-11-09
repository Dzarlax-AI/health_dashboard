package notify

import (
	"strings"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestMorningEnergyFallbackHonorsFinalGuidance(t *testing.T) {
	b := &health.BriefingResponse{Date: "2026-11-09", EnergyBank: &health.EnergyBank{Current: 80, Capacity: 100, ActionVerdict: "push_hard", VerdictReason: "Green light for hard training."}, TodayGuidance: &health.DashboardTodayGuidance{Action: "moderate", Label: "Moderate", Reason: "Confirmed inputs limit intensity.", Summary: "Choose moderate activity."}}
	for _, msg := range []string{formatMorning(b, nil, "en", time.UTC, freshness{}, false), formatMorningRich(b, nil, "en", time.UTC, freshness{}, false, "")} {
		if strings.Contains(msg, "Green light") || strings.Contains(msg, "hard training") {
			t.Fatalf("uncapped Energy action escaped into report: %s", msg)
		}
		if !strings.Contains(msg, "80/100") || !strings.Contains(msg, "Choose moderate activity") {
			t.Fatalf("report lost reserve or final action: %s", msg)
		}
	}
}

func TestMorningReportRetainsFourDomainBlocksWithoutData(t *testing.T) {
	b := &health.BriefingResponse{Date: "2026-11-09"}
	for _, msg := range []string{formatMorning(b, nil, "en", time.UTC, freshness{}, false), formatMorningRich(b, nil, "en", time.UTC, freshness{}, false, "")} {
		previous := -1
		for _, label := range []string{"Sleep", "Recovery today", "Energy as of report", "Activity yesterday"} {
			at := strings.Index(msg, label)
			if at <= previous {
				t.Fatalf("missing or unordered %q: %s", label, msg)
			}
			previous = at
		}
	}
}

func TestMorningStableEpisodeDoesNotWaitForCalendarFinalization(t *testing.T) {
	b := &health.BriefingResponse{Date: "2026-11-09"}
	hours := 6.85
	sleep := health.MorningReportSleep{ReportDate: b.Date, Date: b.Date, Hours: &hours, Capture: health.NightCaptureComplete, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalProvisional, Observed: &health.MorningSleepReadiness{State: health.MorningSleepObservedStable, Hours: hours, Wake: time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC)}}
	for _, msg := range []string{formatMorning(b, nil, "ru", time.UTC, freshness{}, false, sleep), formatMorningRich(b, nil, "ru", time.UTC, freshness{}, false, "", sleep)} {
		for _, forbidden := range []string{"предварительные", "неполные", "не подтверждена", "может измениться"} {
			if strings.Contains(msg, forbidden) {
				t.Fatalf("stable episode inherited a future calendar warning: %s", msg)
			}
		}
		if !strings.Contains(msg, "07:00") || !strings.Contains(msg, "6 ч 51 мин") {
			t.Fatalf("stable episode lost its observed facts: %s", msg)
		}
	}
}

func TestMorningRecordedNightAnalysisSurvivesObservedEpisodeRendering(t *testing.T) {
	b := &health.BriefingResponse{Date: "2026-11-09"}
	hours := 6.85
	sleep := health.MorningReportSleep{ReportDate: b.Date, Date: b.Date, Hours: &hours, Capture: health.NightCaptureComplete, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalProvisional, Observed: &health.MorningSleepReadiness{State: health.MorningSleepObservedStable, Hours: hours, Wake: time.Date(2026, 11, 9, 8, 50, 0, 0, time.UTC)}}
	analysis := "Записанные интервалы бодрствования заняли 15 минут; основная часть окна пришлась на сон."
	for _, msg := range []string{formatMorning(b, map[string]string{"SLEEP": analysis}, "ru", time.UTC, freshness{}, false, sleep), formatMorningRich(b, map[string]string{"SLEEP": analysis}, "ru", time.UTC, freshness{}, false, "", sleep)} {
		if !strings.Contains(msg, analysis) || strings.Contains(msg, "по ней одной нельзя") || strings.Contains(msg, "предварительные") {
			t.Fatalf("recorded-night analysis replaced by a caveat: %s", msg)
		}
	}
}

func TestMorningRecoveryDoesNotPresentWeeklySummaryAsToday(t *testing.T) {
	b := &health.BriefingResponse{Date: "2026-11-09", ReadinessToday: 84, ReadinessTodayBand: "optimal", ReadinessTodayLabel: "Optimal", ReadinessConfidence: health.ReadinessConfidenceFinal, ReadinessServing: &health.ReadinessServingState{Status: health.ReadinessServingFresh, Confidence: health.ReadinessConfidenceFinal}, Sections: []health.BriefingSection{{Key: "recovery", Summary: "Moderate weekly recovery.", Details: []health.BriefingDetail{{Label: "HRV", Value: "38 ms"}}}}}
	for _, msg := range []string{formatMorning(b, nil, "en", time.UTC, freshness{}, false), formatMorningRich(b, nil, "en", time.UTC, freshness{}, false, "")} {
		if strings.Contains(msg, "Moderate weekly recovery") || strings.Contains(msg, "38 ms") || !strings.Contains(msg, "84") {
			t.Fatalf("today report inherited weekly assessment: %s", msg)
		}
	}
}
