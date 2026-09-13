package ai

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
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
			Text: "Several recent nights were shorter than the personal historical reference, forming a pattern rather than describing one night.", ClaimIDs: []string{"recent_sleep_below_reference"}, QualifierIDs: []string{"personal_pattern", "current_context"}, MeaningIDs: []string{"sleep_pattern_not_single_night"},
		}}},
		&health.DailyInsightNarrativeSection{Sentences: []health.DailyInsightNarrativeSentence{{
			Text: "You should rest today.", ClaimIDs: []string{"recovery_current_context"}, QualifierIDs: []string{"current_context"}, MeaningIDs: []string{"recovery_pacing_not_verdict"},
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

func TestGenerateDailyInsightNarrativeSlotSendsOnlyOneClosedPacket(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	provider := &dailyInsightTestProvider{}
	provider.response = `{"version":"today-insight-slot-v2","locale":"en","slot":{"key":"sleep","section":{"sentences":[{"text":"Several recent nights were shorter than the personal historical reference, forming a pattern rather than describing one night.","claim_ids":["recent_sleep_below_reference"],"qualifier_ids":["personal_pattern","current_context"],"meaning_ids":["sleep_pattern_not_single_night"]}]}}}`
	result, err := GenerateDailyInsightNarrativeSlot(context.Background(), provider, ProviderConfig{}, snapshot, "en", "sleep")
	if err != nil || result.Section == nil {
		t.Fatalf("GenerateDailyInsightNarrativeSlot: section=%#v err=%v", result.Section, err)
	}
	var payload health.DailyInsightNarrativeSlotInput
	if err := json.Unmarshal(provider.request.UserPayload, &payload); err != nil {
		t.Fatalf("decode slot packet: %v", err)
	}
	if payload.Slot.Key != "sleep" || len(payload.Slot.Claims) != 1 || payload.Slot.Claims[0].ID != "recent_sleep_below_reference" || len(payload.Slot.Claims[0].MeaningLinks) == 0 {
		t.Fatalf("slot payload = %#v", payload)
	}
	if provider.request.ResponseSchema != dailyInsightNarrativeSlotResponseSchema {
		t.Fatal("provider did not receive independent slot response schema")
	}
}

func TestGenerateDailyInsightNarrativeSlotClassifiesRejectedProseAsSemantic(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	provider := &dailyInsightTestProvider{response: `{"version":"today-insight-slot-v2","locale":"en","slot":{"key":"sleep","section":{"sentences":[{"text":"You should rest today.","claim_ids":["recent_sleep_below_reference"],"qualifier_ids":["personal_pattern","current_context"],"meaning_ids":["sleep_pattern_not_single_night"]}]}}}`}
	_, err := GenerateDailyInsightNarrativeSlot(context.Background(), provider, ProviderConfig{}, snapshot, "en", "sleep")
	var semanticErr *DailyInsightNarrativeSemanticError
	if err == nil || !errors.As(err, &semanticErr) {
		t.Fatalf("GenerateDailyInsightNarrativeSlot error = %v, want semantic rejection", err)
	}
}

func TestDailyInsightNarrativeSlotSchemaUsesStrictObjectKeywords(t *testing.T) {
	root := dailyInsightNarrativeSlotResponseSchema.Schema
	rootProperties, ok := root["properties"].(map[string]any)
	if !ok {
		t.Fatalf("root properties = %#v", root["properties"])
	}
	if _, misplaced := rootProperties["required"]; misplaced || root["required"] == nil || root["additionalProperties"] != false {
		t.Fatalf("root strict schema keywords are misplaced: %#v", root)
	}
	slot, ok := rootProperties["slot"].(map[string]any)
	if !ok {
		t.Fatalf("slot schema = %#v", rootProperties["slot"])
	}
	slotProperties, ok := slot["properties"].(map[string]any)
	if !ok {
		t.Fatalf("slot properties = %#v", slot["properties"])
	}
	if _, misplaced := slotProperties["required"]; misplaced || slot["required"] == nil || slot["additionalProperties"] != false {
		t.Fatalf("slot strict schema keywords are misplaced: %#v", slot)
	}
}

func TestDailyInsightSlotPromptDoesNotInviteForbiddenSafetyDisclaimers(t *testing.T) {
	prompt := strings.ToLower(dailyInsightSlotSystemPrompt)
	for _, fragment := range []string{"diagnos", "prognos", "диагноз", "прогноз", "dijagnoz", "prognoz"} {
		if strings.Contains(prompt, fragment) {
			t.Fatalf("slot prompt contains forbidden narrative fragment %q", fragment)
		}
	}
}

func dailyInsightTestSnapshot(t *testing.T) *health.DailyInsightSnapshot {
	t.Helper()
	duration := 7.2
	base := health.BuildDailyInsightSnapshot(&health.BriefingResponse{
		Date: "2026-09-12", Sleep: &health.SleepAnalysis{LatestDate: "2026-09-12", LatestTotal: &duration, TotalAvg: 6.8},
		ReadinessToday: 70, ReadinessTodayLabel: "Moderate", ReadinessServing: &health.ReadinessServingState{Status: health.ReadinessServingFresh, Confidence: health.ReadinessConfidenceFinal},
		EnergyBank: &health.EnergyBank{Current: 56, Capacity: 80, ActionVerdict: "active_recovery", VerdictReason: "Current reserve is available."},
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
