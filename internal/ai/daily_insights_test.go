package ai

import (
	"testing"

	"health-receiver/internal/health"
)

func TestValidateDailyInsightNarrativeRejectsInvalidDomainAcknowledgement(t *testing.T) {
	snapshot := &health.DailyInsightSnapshot{
		Primary: health.DailyInsight{EvidenceIDs: []string{"primary-evidence"}, Fallback: true},
		Domains: []health.DailyInsightDomain{
			{Key: "sleep", Insight: health.DailyInsight{EvidenceIDs: []string{"sleep-evidence"}}},
		},
	}
	candidate := health.DailyInsightNarrative{
		Primary: health.DailyInsightNarrativeSection{
			Template:    "server_default",
			EvidenceIDs: []string{"primary-evidence"},
		},
		Domains: []health.DailyInsightNarrativeDomain{{
			Key: "sleep",
			DailyInsightNarrativeSection: health.DailyInsightNarrativeSection{
				Template:    "Rest today",
				EvidenceIDs: []string{"sleep-evidence"},
			},
		}},
	}

	validated, invalidDomains, err := validateDailyInsightNarrative(snapshot, candidate)
	if err == nil {
		t.Fatal("invalid domain acknowledgement was accepted")
	}
	if got := invalidDomains["sleep"]; got == "" {
		t.Fatalf("unsafe provider prose was accepted: %#v", validated)
	}
	if len(validated.Domains) != 0 || validated.Primary.Template != "" {
		t.Fatalf("validated partial narrative = %#v, want zero value", validated)
	}
	if _, err := health.ApplyDailyInsightNarrative(snapshot, candidate); err == nil {
		t.Fatal("ApplyDailyInsightNarrative accepted an invalid domain")
	}
}
