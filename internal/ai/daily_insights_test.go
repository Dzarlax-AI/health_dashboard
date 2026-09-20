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

func TestGenerateDailyInsightNarrativeSlotSendsOnlyCombinedOverallPacket(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	provider := &dailyInsightTestProvider{response: dailyInsightTestOverallNarrative(t, "The recommendation brings sleep and recovery together instead of relying on one measure.")}

	result, err := GenerateDailyInsightNarrativeSlot(context.Background(), provider, ProviderConfig{}, snapshot, "en", health.DailyInsightNarrativeOverallSlot)
	if err != nil {
		t.Fatalf("GenerateDailyInsightNarrativeSlot: %v", err)
	}
	if result.Section == nil {
		t.Fatalf("overall slot unexpectedly has no narrative: %#v", result)
	}
	var payload health.DailyInsightNarrativeSlotInput
	if err := json.Unmarshal(provider.request.UserPayload, &payload); err != nil {
		t.Fatalf("decode packet: %v", err)
	}
	if payload.Version != health.DailyInsightNarrativeInputVersion || payload.Slot.Key != health.DailyInsightNarrativeOverallSlot || len(payload.Slot.Claims) != 1 || payload.Slot.Claims[0].MeaningLinks[0].ID != "overall_combined_context" {
		t.Fatalf("provider payload = %#v", payload)
	}
	if len(payload.Slot.Facts) == 0 || payload.Slot.Story == nil {
		t.Fatalf("provider did not receive rich server material: %#v", payload.Slot)
	}
	hasDisplayValue := false
	for _, fact := range payload.Slot.Facts {
		hasDisplayValue = hasDisplayValue || len(fact.DisplayValues) != 0
	}
	if !hasDisplayValue {
		t.Fatalf("provider payload has no server-formatted numeric display values: %#v", payload.Slot.Facts)
	}
	if len(payload.Slot.Claims[0].AnchorVariants) != 0 {
		t.Fatalf("provider payload leaked server-rendered anchors: %#v", payload.Slot.Claims[0])
	}
	if provider.request.ResponseSchema != dailyInsightNarrativeSlotResponseSchema {
		t.Fatal("provider did not receive independent slot response schema")
	}
}

func TestGenerateDailyInsightNarrativeSlotGeneratesDistinctDomainMeaning(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	provider := &dailyInsightTestProvider{response: dailyInsightTestDomainNarrative(t, "sleep", "This is no longer one short night; it is a repeated departure from your usual sleep.", "recent_sleep_below_reference", []string{"personal_pattern", "current_context"}, "sleep_personal_reference")}
	result, err := GenerateDailyInsightNarrativeSlot(context.Background(), provider, ProviderConfig{}, snapshot, "en", "sleep")
	if err != nil || result.Section == nil {
		t.Fatalf("GenerateDailyInsightNarrativeSlot: section=%#v err=%v", result.Section, err)
	}
	var payload health.DailyInsightNarrativeSlotInput
	if err := json.Unmarshal(provider.request.UserPayload, &payload); err != nil {
		t.Fatalf("decode packet: %v", err)
	}
	if payload.Slot.Key != "sleep" || len(payload.Slot.Claims) != 1 || payload.Slot.Claims[0].MeaningLinks[0].ID != "sleep_personal_reference" {
		t.Fatalf("sleep provider payload = %#v", payload)
	}
}

func TestGenerateDailyInsightNarrativeSlotClassifiesRejectedProseAsSemantic(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	provider := &dailyInsightTestProvider{response: dailyInsightTestOverallNarrative(t, "You should rest today.")}
	_, err := GenerateDailyInsightNarrativeSlot(context.Background(), provider, ProviderConfig{}, snapshot, "en", health.DailyInsightNarrativeOverallSlot)
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
	sectionAny, ok := slotProperties["section"].(map[string]any)
	if !ok {
		t.Fatalf("section schema = %#v", slotProperties["section"])
	}
	anyOf, ok := sectionAny["anyOf"].([]any)
	if !ok || len(anyOf) != 2 {
		t.Fatalf("section alternatives = %#v", sectionAny["anyOf"])
	}
	sectionSchema, ok := anyOf[1].(map[string]any)
	if !ok {
		t.Fatalf("non-null section schema = %#v", anyOf[1])
	}
	section, ok := sectionSchema["properties"].(map[string]any)
	if !ok || section["anchor_variant_id"] != nil || section["sentences"] == nil {
		t.Fatalf("server-owned anchor schema = %#v", sectionSchema)
	}
	required, ok := sectionSchema["required"].([]string)
	containsSentences := false
	for _, field := range required {
		containsSentences = containsSentences || field == "sentences"
	}
	if !ok || !containsSentences {
		t.Fatalf("section required fields = %#v", sectionSchema["required"])
	}
	sentences, ok := section["sentences"].(map[string]any)
	if !ok || sentences["maxItems"] != 3 {
		t.Fatalf("section sentence limit = %#v", section["sentences"])
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

func TestDailyInsightSlotPromptKeepsSafeguardsOutOfReaderFacingProse(t *testing.T) {
	prompt := strings.ToLower(dailyInsightSlotSystemPrompt)
	for _, fragment := range []string{"never turn internal safeguards", "limits, scope, uncertainty, reliability, data quality"} {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("slot prompt no longer keeps internal safeguard %q out of reader prose", fragment)
		}
	}
}

func TestDailyInsightSlotPromptRejectsInterfaceMetaVoice(t *testing.T) {
	prompt := strings.ToLower(dailyInsightSlotSystemPrompt)
	for _, fragment := range []string{"shown", "presented", "highlighted", "card", "indicator", "cue", "score"} {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("slot prompt no longer guards interface-meta wording %q", fragment)
		}
	}
}

func TestDailyInsightSlotPromptRejectsAbstractPacingBoilerplate(t *testing.T) {
	prompt := strings.ToLower(dailyInsightSlotSystemPrompt)
	for _, fragment := range []string{"guide, orientation, cue, verdict, score", "today's pace", "how the day is going", "second person", "facts.display_values", "required_qualifier_ids", "exactly one meaning_id", "causal link", "task difficulty", "chance, randomness, reliability"} {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("slot prompt no longer guards abstract pacing boilerplate %q", fragment)
		}
	}
}

func TestDailyInsightSlotPromptKeepsRussianAndSerbianVoiceConsistent(t *testing.T) {
	prompt := strings.ToLower(dailyInsightSlotSystemPrompt)
	for _, fragment := range []string{"informal singular", "never switch to formal plural", "reserve, signal, band, or range"} {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("slot prompt no longer guards conversational localisation %q", fragment)
		}
	}
}

func TestDailyInsightSlotPromptUsesServerPositionWithoutInterfaceVoice(t *testing.T) {
	prompt := strings.ToLower(dailyInsightSlotSystemPrompt)
	for _, fragment := range []string{"server_position", "supporting signal", "position_ids", "never mention a server"} {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("slot prompt no longer defines server position boundary %q", fragment)
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
	base = health.ApplyRecentSleepBelowReference(base, health.RecentSleepBelowReference{State: health.RecentSleepClaimTrue}, "en")
	base.DecisionID = "decision-for-test"
	base.Primary = health.DailyInsight{
		State: "insight", AnswerKind: health.DailyInsightAnswerFactual, EvidenceIDs: []string{base.Domains[1].Insight.EvidenceIDs[0]},
		NextStep: &health.DailyInsightAction{ID: "daily-decision-moderate"}, NarrativeSubject: "moderate",
	}
	base.Domains[1].Insight.AnswerKind = health.DailyInsightAnswerFactual
	base.Domains[1].Insight.ClaimID = "recovery_readiness_context"
	base.DecisionEvidenceDomains = []string{"sleep", "recovery"}
	return base
}

func dailyInsightTestOverallNarrative(t *testing.T, text string) string {
	t.Helper()
	candidate := health.DailyInsightNarrativeSlot{Version: health.DailyInsightNarrativeVersion, Locale: "en", Slot: health.DailyInsightNarrativeDomain{Key: health.DailyInsightNarrativeOverallSlot, Section: &health.DailyInsightNarrativeSection{Sentences: []health.DailyInsightNarrativeSentence{{
		Text: text, ClaimIDs: []string{"overall_daily_decision_context"}, QualifierIDs: []string{"current_context"}, MeaningIDs: []string{"overall_combined_context"}, PositionIDs: []string{},
	}}}}}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal narrative: %v", err)
	}
	return string(encoded)
}

func dailyInsightTestDomainNarrative(t *testing.T, slot, text, claimID string, qualifiers []string, meaningID string) string {
	t.Helper()
	candidate := health.DailyInsightNarrativeSlot{Version: health.DailyInsightNarrativeVersion, Locale: "en", Slot: health.DailyInsightNarrativeDomain{Key: slot, Section: &health.DailyInsightNarrativeSection{Sentences: []health.DailyInsightNarrativeSentence{{
		Text: text, ClaimIDs: []string{claimID}, QualifierIDs: qualifiers, MeaningIDs: []string{meaningID}, PositionIDs: []string{"daily_decision_position"},
	}}}}}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal domain narrative: %v", err)
	}
	return string(encoded)
}
