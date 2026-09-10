package ai

import (
	"testing"

	"health-receiver/internal/health"
)

func TestValidateDailyInsightNarrativeRejectsProviderProse(t *testing.T) {
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
	if err != nil {
		t.Fatalf("primary acknowledgement: %v", err)
	}
	if validated.Primary.Template != "server_default" {
		t.Fatalf("primary template = %q", validated.Primary.Template)
	}
	applied, err := health.ApplyDailyInsightNarrative(snapshot, validated)
	if err != nil {
		t.Fatalf("apply acknowledgement: %v", err)
	}
	if !applied.Primary.Fallback {
		t.Fatal("server-authored copy was incorrectly presented as provider-authored")
	}
	if got := invalidDomains["sleep"]; got == "" {
		t.Fatalf("unsafe provider prose was accepted: %#v", validated)
	}
}
