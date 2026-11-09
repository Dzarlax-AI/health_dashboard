package storage

import (
	"strings"
	"testing"

	"health-receiver/internal/health"
)

func TestInsightCanonicalSleepPolicyWithholdsProvisionalComparisons(t *testing.T) {
	hours, score := 7.2, 85
	b := &health.BriefingResponse{Date: "2026-11-09", Sleep: &health.SleepAnalysis{LatestDate: "2026-11-09", LatestTotal: &hours, TotalAvg: 8}, SleepQuality: &health.SleepQualityBreakdown{ScorePct: &score, Confidence: health.SleepQualityConfidenceFinal}, DailyDecision: &health.DailyDecision{ID: "decision", Mode: "moderate", Label: "Moderate"}}
	canonical := health.MorningReportSleep{Date: b.Date, Hours: &hours, Capture: health.NightCaptureComplete, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalProvisional}
	prepared := briefingForInsightSleep(b, canonical)
	snapshot := health.BuildDailyInsightSnapshot(prepared, "en")
	input, eligible := health.BuildAIInsightInput(snapshot, "en", "sleep", nil)
	if !eligible {
		t.Fatal("safe observed-duration explanation should remain eligible")
	}
	for _, fact := range input.Facts {
		if fact.ID != "sleep_current_recorded_duration" || strings.Contains(fact.Statement, "average") {
			t.Fatalf("provisional night leaked comparison: %+v", fact)
		}
	}
	applyRecordedSleepPresentation(snapshot, canonical, "en")
	input, eligible = health.BuildAIInsightInput(snapshot, "en", "sleep", nil)
	if !eligible || snapshot.Domains[0].DataState != "fresh" || snapshot.Domains[0].Confidence != "provisional" || strings.Contains(snapshot.Domains[0].Insight.Observation, "incomplete") || snapshot.Domains[0].Insight.Remediation != "" {
		t.Fatalf("complete recorded episode presented as missing: %+v", snapshot.Domains[0])
	}
	for _, fact := range input.Facts {
		if fact.ID != "sleep_current_recorded_duration" {
			t.Fatalf("recorded episode leaked completed-night claims: %+v", fact)
		}
		if !strings.Contains(fact.Meaning, "not evidence of missing episode data") || strings.Contains(fact.Statement, "unconfirmed") {
			t.Fatalf("complete capture still described to AI as missing data: %+v", fact)
		}
	}
	candidate := health.AIInsightSlotResponse{Version: health.AIInsightVersion, Locale: "en", Slot: "sleep", Insight: &health.AIInsightSection{Text: "This is a full night of sleep.", Stance: "agree", FactIDs: []string{"sleep_current_recorded_duration"}}}
	if _, err := health.ValidateAIInsightSlot(input, candidate); err == nil {
		t.Fatal("recorded episode granted a finalized-night claim")
	}
	if prepared.DailyDecision != b.DailyDecision || b.SleepQuality.Confidence != health.SleepQualityConfidenceFinal {
		t.Fatal("narrative policy changed canonical action or legacy scoring")
	}
	canonical.Finalization = health.NightFinalFinal
	if briefingForInsightSleep(b, canonical) != b {
		t.Fatal("matching complete final night was unnecessarily degraded")
	}
	other := 8.0
	canonical.Hours = &other
	if briefingForInsightSleep(b, canonical) == b {
		t.Fatal("mismatched legacy duration borrowed canonical finality")
	}
}
