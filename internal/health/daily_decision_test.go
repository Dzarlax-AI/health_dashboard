package health

import "testing"

func TestBuildDailyDecisionUsesFinalEnergyVerdict(t *testing.T) {
	resp := &BriefingResponse{
		Date:                "2026-09-09",
		ReadinessToday:      92,
		ReadinessTodayLabel: "Excellent",
		ReadinessConfidence: ReadinessConfidenceFinal,
		Headline:            &HeadlineSignal{Key: "good_recovery", Severity: "positive"},
		EnergyBank: &EnergyBank{
			ActionVerdict: "active_recovery",
			VerdictLabel:  "Active recovery",
			VerdictReason: "Protect recovery today.",
		},
	}

	got := BuildDailyDecision(resp)
	if got.Mode != "active_recovery" || got.Label != "Active recovery" || got.Reason != "Protect recovery today." {
		t.Fatalf("decision must use the final EnergyBank verdict, got %#v", got)
	}
	if got.ID == "" || len(got.SignalKeys) != 1 || got.SignalKeys[0] != "good_recovery" {
		t.Fatalf("decision must carry a stable ID and its evidence key, got %#v", got)
	}
}

func TestDailyDecisionIDChangesForDecisionRelevantEvidence(t *testing.T) {
	resp := &BriefingResponse{
		Date:                "2026-09-09",
		ReadinessToday:      70,
		ReadinessConfidence: ReadinessConfidenceFinal,
		Headline:            &HeadlineSignal{Key: "stable", Severity: "info"},
		EnergyBank:          &EnergyBank{ActionVerdict: "moderate"},
	}
	first := BuildDailyDecision(resp).ID
	resp.Headline.Key = "depressed_hrv"
	second := BuildDailyDecision(resp).ID
	if first == second {
		t.Fatal("decision ID did not change after its headline evidence changed")
	}
}

func TestDailyDecisionIDChangesWhenRationaleChanges(t *testing.T) {
	resp := &BriefingResponse{
		Date:                "2026-09-09",
		ReadinessToday:      70,
		ReadinessConfidence: ReadinessConfidenceFinal,
		EnergyBank: &EnergyBank{
			ActionVerdict: "moderate",
			VerdictLabel:  "Moderate",
			VerdictReason: "Keep the effort comfortable.",
		},
	}
	first := BuildDailyDecision(resp).ID
	resp.EnergyBank.VerdictReason = "Prefer easy movement after poor sleep."
	second := BuildDailyDecision(resp).ID
	if first == second {
		t.Fatal("decision ID did not change after its rationale changed")
	}
}
