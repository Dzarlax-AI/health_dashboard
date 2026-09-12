package ai

import (
	"context"
	"encoding/json"
	"testing"

	"health-receiver/internal/health"
)

type dailyInsightTestProvider struct {
	response string
	request  GenerationRequest
}

func (p *dailyInsightTestProvider) Descriptor() ProviderDescriptor {
	return ProviderDescriptor{ID: "test"}
}
func (p *dailyInsightTestProvider) ListModels(context.Context, string) ([]Model, error) {
	return nil, nil
}
func (p *dailyInsightTestProvider) Generate(_ context.Context, _ ProviderConfig, request GenerationRequest) (GenerationResult, error) {
	p.request = request
	return GenerationResult{Text: p.response}, nil
}

func TestGenerateDailyInsightNarrativeUsesClaimPacketAndKeepsInvalidDomainFallback(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	provider := &dailyInsightTestProvider{}
	provider.response = dailyInsightTestNarrative(t,
		&health.DailyInsightNarrativeSection{Sentences: []health.DailyInsightNarrativeSentence{{
			Text: "It gives the current day a little more context.", ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"},
		}}},
		&health.DailyInsightNarrativeSection{Sentences: []health.DailyInsightNarrativeSentence{{
			Text: "You should rest today.", ClaimIDs: []string{"recovery_current_context"}, QualifierIDs: []string{"current_context"},
		}}},
	)

	result, err := GenerateDailyInsightNarrative(context.Background(), provider, ProviderConfig{}, snapshot, "en")
	if err != nil {
		t.Fatalf("GenerateDailyInsightNarrative: %v", err)
	}
	if result.InvalidDomains["recovery"] == "" {
		t.Fatalf("unsafe recovery prose was accepted: %#v", result.Narrative)
	}
	if result.Narrative.Domains[0].Section == nil || result.Narrative.Domains[1].Section != nil {
		t.Fatalf("partial narrative = %#v", result.Narrative)
	}
	var payload health.DailyInsightNarrativeInput
	if err := json.Unmarshal(provider.request.UserPayload, &payload); err != nil {
		t.Fatalf("decode packet: %v", err)
	}
	if payload.Version != health.DailyInsightNarrativeInputVersion || len(payload.Domains) != 3 || payload.Domains[0].Claims[0].Proposition == "" {
		t.Fatalf("provider payload = %#v", payload)
	}
	if provider.request.ResponseSchema != dailyInsightNarrativeResponseSchema {
		t.Fatal("provider did not receive NarrativeV3 schema")
	}
}

func dailyInsightTestSnapshot(t *testing.T) *health.DailyInsightSnapshot {
	t.Helper()
	duration := 7.2
	base := health.BuildDailyInsightSnapshot(&health.BriefingResponse{
		Date: "2026-09-12", Sleep: &health.SleepAnalysis{LatestDate: "2026-09-12", LatestTotal: &duration, TotalAvg: 6.8},
		ReadinessToday: 70, ReadinessTodayLabel: "Moderate", ReadinessServing: &health.ReadinessServingState{Status: health.ReadinessServingFresh, Confidence: health.ReadinessConfidenceFinal},
		EnergyBank: &health.EnergyBank{Current: 56, Capacity: 80, ActionVerdict: "moderate", VerdictReason: "Current reserve is available."},
	}, "en")
	return health.ApplyRecentSleepBelowReference(base, health.RecentSleepBelowReference{State: health.RecentSleepClaimTrue}, "en")
}

func dailyInsightTestNarrative(t *testing.T, sleep, recovery *health.DailyInsightNarrativeSection) string {
	t.Helper()
	candidate := health.DailyInsightNarrative{Version: health.DailyInsightNarrativeVersion, Locale: "en", Domains: []health.DailyInsightNarrativeDomain{
		{Key: "sleep", Section: sleep}, {Key: "recovery", Section: recovery}, {Key: "energy", Section: nil},
	}}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal narrative: %v", err)
	}
	return string(encoded)
}
