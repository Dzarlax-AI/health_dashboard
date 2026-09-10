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

func TestDailyDecisionEvidenceDomainsFollowFinalGuidanceCap(t *testing.T) {
	resp := &BriefingResponse{
		Date:               "2026-09-10",
		ReadinessCapReason: "missing_same_day_evidence",
		EnergyBank: &EnergyBank{
			ActionVerdict: "moderate",
			VerdictReason: "Energy is healthy.",
		},
		SleepQuality: &SleepQualityBreakdown{Confidence: SleepQualityConfidenceLow},
		TodayGuidance: &DashboardTodayGuidance{
			Action: "rest",
			Reason: "Сон пока не даёт полной уверенности.",
		},
	}
	decision := BuildDailyDecision(resp)
	if got, want := decision.EvidenceDomains, []string{"sleep"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("evidence domains = %#v, want %#v", got, want)
	}

	domains := []DailyInsightDomain{
		{Key: "sleep", Insight: DailyInsight{EvidenceIDs: []string{"sleep-evidence"}}},
		{Key: "recovery", Insight: DailyInsight{EvidenceIDs: []string{"recovery-evidence"}}},
		{Key: "energy", Insight: DailyInsight{EvidenceIDs: []string{"energy-evidence"}}},
	}
	primary := choosePrimaryInsight(resp, decision, domains, dailyInsightCopy("en"))
	if got, want := primary.EvidenceIDs, []string{"sleep-evidence"}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("primary evidence = %#v, want %#v", got, want)
	}
}
