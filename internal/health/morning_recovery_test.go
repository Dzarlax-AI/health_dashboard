package health

import (
	"strings"
	"testing"
)

func TestMorningRecoveryContextUsesCurrentConfirmedFacts(t *testing.T) {
	date, hrv := "2026-11-09", 43.6
	b := &BriefingResponse{Date: date, ReadinessToday: 84, ReadinessTodayBand: "optimal", ReadinessTodayLabel: "Optimal", ReadinessConfidence: ReadinessConfidenceFinal, ReadinessServing: &ReadinessServingState{Status: ReadinessServingFresh, Confidence: ReadinessConfidenceFinal}, Sections: []BriefingSection{{Key: "recovery", Summary: "Moderate weekly recovery.", Details: []BriefingDetail{{Label: "HRV", Value: "38 ms"}}}}, RawMetrics: &RawMetrics{LastDate: date, ReadinessEvidence: &ReadinessEvidenceInput{Date: date, HRV: ReadinessComponentEvidence{Metric: "heart_rate_variability", Value: &hrv, Present: true, EvaluatedDate: date, SourceDate: date, Freshness: ReadinessFreshnessOK, Confidence: ReadinessConfidenceFinal, SampleCount: MinSleepWindowHRVSamplesForFullConfidence}}}}
	for _, lang := range []string{"en", "ru", "sr"} {
		summary, facts, ok := MorningRecoveryContext(b, lang)
		if !ok || !strings.Contains(summary, "84") || len(facts) != 1 || !strings.Contains(facts[0], "43.6") || strings.Contains(summary, "Moderate") || strings.Contains(strings.Join(facts, " "), "38 ms") {
			t.Fatalf("%s current recovery conflated with weekly section: %q %v %v", lang, summary, facts, ok)
		}
	}
	b.RawMetrics.ReadinessEvidence.HRV.Confidence = ReadinessConfidenceLow
	if _, facts, ok := MorningRecoveryContext(b, "en"); !ok || len(facts) != 0 {
		t.Fatalf("unconfirmed component presented as current: %v", facts)
	}
	b.ReadinessServing.Status = ReadinessServingStale
	if _, _, ok := MorningRecoveryContext(b, "en"); ok {
		t.Fatal("stale readiness borrowed current presentation")
	}
}
