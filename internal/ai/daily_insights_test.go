package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/health"
)

type dailyInsightTestProvider struct {
	response  string
	responses []string
	errors    []error
	request   GenerationRequest
	requests  []GenerationRequest
	configs   []ProviderConfig
}

func (p *dailyInsightTestProvider) Descriptor() ProviderDescriptor {
	return ProviderDescriptor{ID: "test"}
}
func (p *dailyInsightTestProvider) ListModels(context.Context, string) ([]Model, error) {
	return nil, nil
}
func (p *dailyInsightTestProvider) Generate(_ context.Context, cfg ProviderConfig, request GenerationRequest) (GenerationResult, error) {
	index := len(p.requests)
	p.requests = append(p.requests, request)
	p.configs = append(p.configs, cfg)
	if index == 0 {
		// Existing generation assertions inspect this field; retain the first
		// call while requests captures the independent safety review too.
		p.request = request
	}
	if index < len(p.errors) && p.errors[index] != nil {
		return GenerationResult{}, p.errors[index]
	}
	if index < len(p.responses) {
		return GenerationResult{Text: p.responses[index], RequestID: fmt.Sprintf("request-%d", index+1), InputTokens: int64(100 + index), OutputTokens: int64(10 + index), TotalTokens: int64(110 + 2*index), FinishReason: "completed", Attempts: 1, Latency: time.Duration(index+1) * time.Millisecond}, nil
	}
	if index == 0 {
		return GenerationResult{Text: p.response, RequestID: "request-1", InputTokens: 100, OutputTokens: 10, TotalTokens: 110, FinishReason: "completed", Attempts: 1}, nil
	}
	return GenerationResult{Text: dailyInsightTestSafetyReview("allow"), RequestID: "request-2", InputTokens: 101, OutputTokens: 11, TotalTokens: 112, FinishReason: "completed", Attempts: 1}, nil
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
	if result.SafetyEvidence == nil || result.SafetyEvidence.CandidateHash != DailyInsightNarrativeSafetyCandidateHash("en", result.Section) || result.SafetyEvidence.Verdict != "allow" || result.SafetyEvidence.Categories == nil || len(result.SafetyEvidence.Categories) != 0 || result.SafetyEvidence.SafetyPromptRevision != DailyInsightNarrativeSafetyReviewPromptRevision || result.SafetyEvidence.SafetyMaxOutputTokens != DailyInsightNarrativeSafetyReviewMaxTokens || result.SafetyEvidence.ReviewFingerprint != DailyInsightNarrativeCurrentReviewIdentity().Fingerprint || result.SafetyEvidence.ProviderRequestID != "request-2" || result.SafetyEvidence.ProviderInputTokens != 101 || result.SafetyEvidence.ProviderOutputTokens != 11 || result.SafetyEvidence.ProviderAttempts != 1 {
		t.Fatalf("missing bound safety evidence: %#v", result.SafetyEvidence)
	}
	receiptJSON, err := json.Marshal(result.SafetyEvidence)
	if err != nil || !strings.Contains(string(receiptJSON), `"categories":[]`) {
		t.Fatalf("allow safety evidence did not serialize explicit categories: %s err=%v", receiptJSON, err)
	}
	var payload health.DailyInsightNarrativeSlotInput
	if err := json.Unmarshal(provider.request.UserPayload, &payload); err != nil {
		t.Fatalf("decode packet: %v", err)
	}
	if payload.Version != health.DailyInsightNarrativeInputVersion || payload.Slot.Key != health.DailyInsightNarrativeOverallSlot || len(payload.Slot.Facts) < 2 || payload.Slot.Baseline == nil {
		t.Fatalf("provider payload = %#v", payload)
	}
	if len(payload.Slot.ActionOptions) > 2 {
		t.Fatalf("provider did not receive rich server material: %#v", payload.Slot)
	}
	hasDisplayValue := false
	for _, fact := range payload.Slot.Facts {
		hasDisplayValue = hasDisplayValue || len(fact.DisplayValues) != 0
	}
	if !hasDisplayValue {
		t.Fatalf("provider payload has no server-formatted numeric display values: %#v", payload.Slot.Facts)
	}
	encoded := string(provider.request.UserPayload)
	for _, forbidden := range []string{"source", "device", "components", "health_records", "tenant", "schema"} {
		if strings.Contains(strings.ToLower(encoded), forbidden) {
			t.Fatalf("provider payload leaked %q: %s", forbidden, encoded)
		}
	}
	if provider.request.ResponseSchema != dailyInsightNarrativeSlotResponseSchema {
		t.Fatal("provider did not receive independent slot response schema")
	}
	if len(provider.requests) != 2 || provider.requests[1].Prompt != dailyInsightNarrativeSafetyReviewPrompt || provider.requests[1].ResponseSchema != dailyInsightNarrativeSafetyReviewResponseSchema || provider.configs[1].MaxOutputTokens != DailyInsightNarrativeSafetyReviewMaxTokens {
		t.Fatalf("generation did not make the bounded independent safety call: requests=%#v configs=%#v", provider.requests, provider.configs)
	}
	var safetyPayload dailyInsightNarrativeSafetyReviewInput
	if err := json.Unmarshal(provider.requests[1].UserPayload, &safetyPayload); err != nil {
		t.Fatalf("decode safety packet: %v", err)
	}
	if safetyPayload.Locale != "en" || safetyPayload.Candidate.Text != result.Section.Text || len(safetyPayload.Candidate.FactIDs) < 2 || len(safetyPayload.Facts) < 2 || safetyPayload.Facts[0].Statement != payload.Slot.Facts[0].Statement || len(safetyPayload.ActionOptions) != len(payload.Slot.ActionOptions) || len(safetyPayload.PolicyCategories) != len(dailyInsightNarrativeSafetyCategories) {
		t.Fatalf("safety payload = %#v", safetyPayload)
	}
	for _, forbidden := range []string{"source", "device", "components", "health_records", "tenant", "schema", "raw_metrics"} {
		if strings.Contains(strings.ToLower(string(provider.requests[1].UserPayload)), forbidden) {
			t.Fatalf("safety payload leaked %q: %s", forbidden, provider.requests[1].UserPayload)
		}
	}
}

func TestGenerateDailyInsightNarrativeSlotFromInputSendsExactFrozenContext(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	item := DailyInsightNarrativeCorpusCase{
		ID: "frozen-context", Locale: "en", Snapshot: *snapshot,
		NarrativeFacts: append([]health.DailyInsightNarrativeFact(nil), snapshot.NarrativeFacts...),
		VisibleB0Baseline: &health.DailyInsightNarrativeBaseline{
			Primary: "FROZEN B0 PRIMARY",
			Domains: []health.DailyInsightBaselineDomain{{Domain: "sleep", Summary: "FROZEN SLEEP", Observation: "FROZEN OBS", Meaning: "FROZEN MEANING"}},
		},
		ActionOptions: []health.DailyInsightNarrativeAction{{ID: "daily-decision-rest", Text: "FROZEN ACTION"}},
	}
	input, known, err := BuildDailyInsightNarrativeCorpusSlotInput(item, "en", health.DailyInsightNarrativeOverallSlot)
	if err != nil || !known || len(input.Slot.Facts) < 2 {
		t.Fatalf("BuildDailyInsightNarrativeCorpusSlotInput: known=%v err=%v input=%#v", known, err, input)
	}
	factIDs := []string{input.Slot.Facts[0].ID, input.Slot.Facts[1].ID}
	response, err := json.Marshal(health.DailyInsightNarrativeSlot{
		Version: health.DailyInsightNarrativeVersion, Locale: "en",
		Slot: health.DailyInsightNarrativeDomain{Key: health.DailyInsightNarrativeOverallSlot, Section: &health.DailyInsightNarrativeSection{
			Text: "Sleep and recovery are telling a more useful story together.", FactIDs: factIDs, ActionID: "daily-decision-rest",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &dailyInsightTestProvider{response: string(response)}
	result, err := GenerateDailyInsightNarrativeSlotFromInput(context.Background(), provider, ProviderConfig{}, snapshot, "en", health.DailyInsightNarrativeOverallSlot, input)
	if err != nil || result.Section == nil {
		t.Fatalf("GenerateDailyInsightNarrativeSlotFromInput: section=%#v err=%v", result.Section, err)
	}
	var captured health.DailyInsightNarrativeSlotInput
	if err := json.Unmarshal(provider.request.UserPayload, &captured); err != nil {
		t.Fatal(err)
	}
	if captured.Slot.Baseline == nil || captured.Slot.Baseline.Primary != "FROZEN B0 PRIMARY" || len(captured.Slot.Baseline.Domains) != 1 || captured.Slot.Baseline.Domains[0].Summary != "FROZEN SLEEP" {
		t.Fatalf("provider rebuilt rather than sent frozen B0 baseline: %#v", captured.Slot.Baseline)
	}
	if len(captured.Slot.ActionOptions) != 1 || captured.Slot.ActionOptions[0].ID != "daily-decision-rest" || captured.Slot.ActionOptions[0].Text != "FROZEN ACTION" {
		t.Fatalf("provider rebuilt rather than sent frozen action options: %#v", captured.Slot.ActionOptions)
	}
}

func TestGenerateDailyInsightNarrativeSlotFromInputCanonicalizesUnsupportedLocaleToEnglish(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	input, known := health.BuildDailyInsightNarrativeSlotInput(snapshot, "en", health.DailyInsightNarrativeOverallSlot)
	if !known {
		t.Fatal("overall slot is not known")
	}
	provider := &dailyInsightTestProvider{response: dailyInsightTestOverallNarrative(t, "The recommendation brings sleep and recovery together instead of relying on one measure.")}
	result, err := GenerateDailyInsightNarrativeSlotFromInput(context.Background(), provider, ProviderConfig{}, snapshot, "de", health.DailyInsightNarrativeOverallSlot, input)
	if err != nil || result.Section == nil {
		t.Fatalf("GenerateDailyInsightNarrativeSlotFromInput: section=%#v err=%v", result.Section, err)
	}
	if len(provider.requests) != 2 || provider.requests[0].Language != "en" || provider.requests[1].Language != "en" {
		t.Fatalf("unsupported locale did not use canonical English provider requests: %#v", provider.requests)
	}
	var captured health.DailyInsightNarrativeSlotInput
	if err := json.Unmarshal(provider.requests[0].UserPayload, &captured); err != nil {
		t.Fatalf("decode frozen input: %v", err)
	}
	if captured.Locale != "en" {
		t.Fatalf("frozen input locale = %q, want canonical en", captured.Locale)
	}
}

func TestGenerateDailyInsightNarrativeSlotSkipsStandaloneSleepPattern(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	provider := &dailyInsightTestProvider{response: dailyInsightTestDomainNarrative(t, "sleep", "This is no longer one short night; it is a repeated departure from your usual sleep.", "recent_sleep_below_reference", []string{"personal_pattern", "current_context"}, "sleep_personal_reference")}
	result, err := GenerateDailyInsightNarrativeSlot(context.Background(), provider, ProviderConfig{}, snapshot, "en", "sleep")
	if err != nil || result.Section != nil {
		t.Fatalf("GenerateDailyInsightNarrativeSlot: section=%#v err=%v", result.Section, err)
	}
	if provider.request.Prompt != "" {
		t.Fatal("provider was called for a standalone sleep pattern")
	}
}

func TestGenerateDailyInsightNarrativeSlotSkipsStandaloneRecoveryCard(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	snapshot.DecisionEvidenceDomains = []string{"recovery"}
	snapshot.Primary.EvidenceIDs = append([]string(nil), snapshot.Domains[1].Insight.EvidenceIDs...)
	provider := &dailyInsightTestProvider{response: dailyInsightTestDomainNarrative(t, "recovery", "Recovery is high today.", "recovery_readiness_context", []string{"current_context"}, "recovery_current_context")}

	result, err := GenerateDailyInsightNarrativeSlot(context.Background(), provider, ProviderConfig{}, snapshot, "en", "recovery")
	if err != nil || result.Section != nil {
		t.Fatalf("GenerateDailyInsightNarrativeSlot: section=%#v err=%v", result.Section, err)
	}
	if provider.request.Prompt != "" {
		t.Fatal("provider was called for a standalone recovery card")
	}
}

func TestGenerateDailyInsightNarrativeSlotClassifiesRejectedProseAsSemantic(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	provider := &dailyInsightTestProvider{response: dailyInsightTestOverallNarrative(t, "Take a supplement dose today.")}
	_, err := GenerateDailyInsightNarrativeSlot(context.Background(), provider, ProviderConfig{}, snapshot, "en", health.DailyInsightNarrativeOverallSlot)
	var semanticErr *DailyInsightNarrativeSemanticError
	if err == nil || !errors.As(err, &semanticErr) {
		t.Fatalf("GenerateDailyInsightNarrativeSlot error = %v, want semantic rejection", err)
	}
}

func TestGenerateDailyInsightNarrativeSlotRequiresSafetyAllowForNovelSuggestion(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	candidate := dailyInsightTestOverallNarrative(t, "Sleep and readiness point to a quieter frame, so you could take a short walk if it fits.")
	provider := &dailyInsightTestProvider{responses: []string{candidate, dailyInsightTestSafetyReview("allow")}}
	result, err := GenerateDailyInsightNarrativeSlot(context.Background(), provider, ProviderConfig{MaxOutputTokens: 1500}, snapshot, "en", health.DailyInsightNarrativeOverallSlot)
	if err != nil || result.Section == nil || result.SafetyEvidence == nil || len(provider.requests) != 2 {
		t.Fatalf("allowed novel suggestion: section=%#v requests=%d err=%v", result.Section, len(provider.requests), err)
	}
}

func TestGenerateDailyInsightNarrativeSlotSafetyReviewerRejectsMedicationExtremeAndConflict(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	for _, test := range []struct {
		name     string
		text     string
		category string
	}{
		{"aspirin", "Sleep and readiness point together, so consider aspirin if it fits.", "medication_supplement_dose"},
		{"extreme", "Sleep and readiness point together, so a demanding interval session could fit today.", "extreme_exercise_restriction"},
		{"conflict", "Sleep and readiness point together, so an extra-long late evening could fit.", "contradicts_server_action"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := dailyInsightTestOverallNarrative(t, test.text)
			var decoded health.DailyInsightNarrativeSlot
			if err := json.Unmarshal([]byte(candidate), &decoded); err != nil {
				t.Fatal(err)
			}
			input, known := health.BuildDailyInsightNarrativeSlotInput(snapshot, "en", health.DailyInsightNarrativeOverallSlot)
			if !known {
				t.Fatal("missing overall input")
			}
			if _, err := health.ValidateDailyInsightNarrativeSlotResponseWithInput(snapshot, "en", health.DailyInsightNarrativeOverallSlot, input.Slot, decoded); err != nil {
				t.Fatalf("fixture must reach semantic safety review: %v", err)
			}
			provider := &dailyInsightTestProvider{responses: []string{candidate, dailyInsightTestSafetyReject(test.category)}}
			result, err := GenerateDailyInsightNarrativeSlot(context.Background(), provider, ProviderConfig{}, snapshot, "en", health.DailyInsightNarrativeOverallSlot)
			var semanticErr *DailyInsightNarrativeSemanticError
			if err == nil || !errors.As(err, &semanticErr) || result.Section != nil || result.Text != "" || result.SafetyEvidence == nil || result.SafetyEvidence.Verdict != "reject" || len(result.SafetyEvidence.Categories) != 1 || result.SafetyEvidence.Categories[0] != test.category {
				t.Fatalf("unsafe %s was not fail-closed with receipt: result=%#v err=%v", test.name, result, err)
			}
		})
	}
}

func TestGenerateDailyInsightNarrativeSlotSafetyReviewerRejectsAdversarialParaphrases(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	for _, test := range []struct {
		name     string
		text     string
		category string
	}{
		{"capacity", "Sleep and readiness mean your body has plenty in reserve for an intense session.", "claimed_feelings_capacity"},
		{"hidden state", "Sleep and readiness show that your body is settling into a gentler operating mode.", "confident_health_energy_recovery_outcome"},
		{"causal paraphrase", "The contrast in sleep and readiness is what repairs your recovery today.", "strong_unsupported_causality"},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := dailyInsightTestOverallNarrative(t, test.text)
			var decoded health.DailyInsightNarrativeSlot
			if err := json.Unmarshal([]byte(candidate), &decoded); err != nil {
				t.Fatal(err)
			}
			input, known := health.BuildDailyInsightNarrativeSlotInput(snapshot, "en", health.DailyInsightNarrativeOverallSlot)
			if !known {
				t.Fatal("missing overall input")
			}
			if _, err := health.ValidateDailyInsightNarrativeSlotResponseWithInput(snapshot, "en", health.DailyInsightNarrativeOverallSlot, input.Slot, decoded); err != nil {
				t.Fatalf("fixture must exercise semantic reviewer beyond lexical boundary: %v", err)
			}
			provider := &dailyInsightTestProvider{responses: []string{candidate, dailyInsightTestSafetyReject(test.category)}}
			_, err := GenerateDailyInsightNarrativeSlot(context.Background(), provider, ProviderConfig{}, snapshot, "en", health.DailyInsightNarrativeOverallSlot)
			var semanticErr *DailyInsightNarrativeSemanticError
			if err == nil || !errors.As(err, &semanticErr) || len(provider.requests) != 2 {
				t.Fatalf("adversarial paraphrase was not fail-closed: requests=%d err=%v", len(provider.requests), err)
			}
		})
	}
}

func TestGenerateDailyInsightNarrativeSlotSafetyReviewerFailsClosed(t *testing.T) {
	snapshot := dailyInsightTestSnapshot(t)
	candidate := dailyInsightTestOverallNarrative(t, "Sleep and readiness point in one direction, so a short walk could fit if it feels useful.")
	for _, test := range []struct {
		name   string
		result string
		err    error
	}{
		{"transport", "", errors.New("review unavailable")},
		{"malformed", "not-json", nil},
		{"unknown category", `{"version":"daily-insight-safety-review-v1","verdict":"reject","categories":["unknown"]}`, nil},
		{"reject", dailyInsightTestSafetyReject("strong_unsupported_causality"), nil},
		{"mismatch", `{"version":"daily-insight-safety-review-v1","verdict":"allow","categories":["claimed_feelings_capacity"]}`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider := &dailyInsightTestProvider{responses: []string{candidate, test.result}, errors: []error{nil, test.err}}
			result, err := GenerateDailyInsightNarrativeSlot(context.Background(), provider, ProviderConfig{}, snapshot, "en", health.DailyInsightNarrativeOverallSlot)
			var semanticErr *DailyInsightNarrativeSemanticError
			if err == nil || !errors.As(err, &semanticErr) || len(provider.requests) != 2 || result.Section != nil || result.Text != "" {
				t.Fatalf("safety %s did not fail closed: requests=%d err=%v", test.name, len(provider.requests), err)
			}
			if test.name == "reject" && (result.SafetyEvidence == nil || result.SafetyEvidence.Verdict != "reject" || len(result.SafetyEvidence.Categories) != 1 || result.SafetyEvidence.Categories[0] != "strong_unsupported_causality") {
				t.Fatalf("decoded reject did not retain payload-free safety receipt: %#v", result.SafetyEvidence)
			}
		})
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
	if !ok || section["anchor_variant_id"] != nil || section["text"] == nil || section["fact_ids"] == nil || section["action_id"] == nil {
		t.Fatalf("server-owned anchor schema = %#v", sectionSchema)
	}
	required, ok := sectionSchema["required"].([]string)
	containsText := false
	for _, field := range required {
		containsText = containsText || field == "text"
	}
	if !ok || !containsText {
		t.Fatalf("section required fields = %#v", sectionSchema["required"])
	}
}

func TestDailyInsightNarrativeSafetyReviewSchemaIsStrictAndClosed(t *testing.T) {
	root := dailyInsightNarrativeSafetyReviewResponseSchema.Schema
	properties, ok := root["properties"].(map[string]any)
	if !ok || root["additionalProperties"] != false {
		t.Fatalf("safety root schema = %#v", root)
	}
	if properties["version"] == nil || properties["verdict"] == nil || properties["categories"] == nil {
		t.Fatalf("safety schema missing fields: %#v", properties)
	}
	items, ok := properties["categories"].(map[string]any)["items"].(map[string]any)
	if !ok {
		t.Fatalf("safety category schema = %#v", properties["categories"])
	}
	allowed, ok := items["enum"].([]string)
	if !ok || len(allowed) != len(dailyInsightNarrativeSafetyCategories) {
		t.Fatalf("safety categories are not closed: %#v", items)
	}
}

func TestDailyInsightNarrativeReviewIdentityTracksValidatorContract(t *testing.T) {
	identity := DailyInsightNarrativeCurrentReviewIdentity()
	if identity.PromptRevision != DailyInsightNarrativeSlotPromptRevision || identity.SafetyPromptRevision != DailyInsightNarrativeSafetyReviewPromptRevision || identity.SafetyMaxOutputTokens != DailyInsightNarrativeSafetyReviewMaxTokens || identity.ClaimPacketVersion != health.DailyInsightNarrativeInputVersion || identity.NarrativeVersion != health.DailyInsightNarrativeVersion || len(identity.Fingerprint) != 64 {
		t.Fatalf("review identity = %#v", identity)
	}
	if identity.ClaimPacketVersion != "today-insight-synthesis-input-v23" {
		t.Fatalf("claim packet version = %q, want freshness-gated packet version", identity.ClaimPacketVersion)
	}
	if DailyInsightNarrativeSafetyReviewPromptRevision != "today-overall-human-safety-review-v2" || DailyInsightNarrativeSafetyReviewMaxTokens != 1024 {
		t.Fatalf("safety review contract = revision %q cap %d", DailyInsightNarrativeSafetyReviewPromptRevision, DailyInsightNarrativeSafetyReviewMaxTokens)
	}
	changedPrompt := dailyInsightNarrativeSlotReviewIdentity(dailyInsightSlotSystemPrompt, dailyInsightNarrativeSlotResponseSchema, DailyInsightNarrativeSlotPromptRevision, dailyInsightNarrativeSafetyReviewPrompt+" changed", dailyInsightNarrativeSafetyReviewResponseSchema, DailyInsightNarrativeSafetyReviewPromptRevision, DailyInsightNarrativeSafetyReviewMaxTokens)
	if changedPrompt.Fingerprint == identity.Fingerprint {
		t.Fatal("safety prompt change retained review identity")
	}
	changedSchema := &ResponseSchema{Name: "changed", Schema: dailyInsightNarrativeSafetyReviewResponseSchema.Schema}
	changedSchemaIdentity := dailyInsightNarrativeSlotReviewIdentity(dailyInsightSlotSystemPrompt, dailyInsightNarrativeSlotResponseSchema, DailyInsightNarrativeSlotPromptRevision, dailyInsightNarrativeSafetyReviewPrompt, changedSchema, DailyInsightNarrativeSafetyReviewPromptRevision, DailyInsightNarrativeSafetyReviewMaxTokens)
	if changedSchemaIdentity.Fingerprint == identity.Fingerprint {
		t.Fatal("safety schema change retained review identity")
	}
	changedCapIdentity := dailyInsightNarrativeSlotReviewIdentity(dailyInsightSlotSystemPrompt, dailyInsightNarrativeSlotResponseSchema, DailyInsightNarrativeSlotPromptRevision, dailyInsightNarrativeSafetyReviewPrompt, dailyInsightNarrativeSafetyReviewResponseSchema, DailyInsightNarrativeSafetyReviewPromptRevision, DailyInsightNarrativeSafetyReviewMaxTokens-1)
	if changedCapIdentity.Fingerprint == identity.Fingerprint {
		t.Fatal("safety token cap change retained review identity")
	}
}

func TestDailyInsightSlotPromptDefinesHumanSynthesisBoundary(t *testing.T) {
	prompt := strings.ToLower(dailyInsightSlotSystemPrompt)
	for _, fragment := range []string{"overall-only", "visible b0", "two domains", "75 words", "action_options", "diagnose", "causality", "serbian latin", "low-risk, reversible everyday suggestion", "action_id is reserved", "soft, uncertain wording", "two-period averages", "stability, steadiness, or consistency", "hidden body state or mode", "dismiss urgent or professional care", "ordinary soft future wording is fine", "without claiming what it will achieve"} {
		if !strings.Contains(prompt, fragment) {
			t.Fatalf("slot prompt no longer defines synthesis boundary %q", fragment)
		}
	}
	if DailyInsightNarrativeSlotPromptRevision != "today-overall-human-synthesis-v39" {
		t.Fatalf("prompt revision = %q, want forecast-boundary contract revision", DailyInsightNarrativeSlotPromptRevision)
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
	candidate := health.DailyInsightNarrativeSlot{Version: health.DailyInsightNarrativeVersion, Locale: "en", Slot: health.DailyInsightNarrativeDomain{Key: health.DailyInsightNarrativeOverallSlot, Section: &health.DailyInsightNarrativeSection{Text: text, FactIDs: []string{"sleep_canonical_comparison", "readiness_current"}}}}
	encoded, err := json.Marshal(candidate)
	if err != nil {
		t.Fatalf("marshal narrative: %v", err)
	}
	return string(encoded)
}

func dailyInsightTestSafetyReview(verdict string) string {
	return fmt.Sprintf(`{"version":%q,"verdict":%q,"categories":[]}`, dailyInsightNarrativeSafetyReviewVersion, verdict)
}

func dailyInsightTestSafetyReject(category string) string {
	return fmt.Sprintf(`{"version":%q,"verdict":"reject","categories":[%q]}`, dailyInsightNarrativeSafetyReviewVersion, category)
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
