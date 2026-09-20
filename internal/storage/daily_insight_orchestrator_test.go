package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

func TestDailyInsightLeaseCoversGenerationDeadline(t *testing.T) {
	if dailyInsightLeaseDuration <= dailyInsightGenerationDeadline {
		t.Fatalf("lease %s must exceed generation deadline %s", dailyInsightLeaseDuration, dailyInsightGenerationDeadline)
	}
}

const testNarrativeSlotProviderID = "storage-test-narrative-slot"

type testNarrativeSlotProvider struct{}

func (testNarrativeSlotProvider) Descriptor() ai.ProviderDescriptor {
	return ai.ProviderDescriptor{ID: testNarrativeSlotProviderID, DisplayName: "Storage test", DefaultModel: "test-slot-model"}
}

func (testNarrativeSlotProvider) ListModels(context.Context, string) ([]ai.Model, error) {
	return nil, nil
}

func (testNarrativeSlotProvider) Generate(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error) {
	return ai.GenerationResult{}, errors.New("test provider intentionally does not generate")
}

func init() {
	ai.RegisterProvider(testNarrativeSlotProvider{})
}

func TestDomainSlotsAllocateLifecycleRowsOnlyForDistinctMeanings(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	identity := ai.DailyInsightNarrativeSlotCurrentReviewIdentity()
	if err := db.SaveTodayInsightsB1QualityGateApproval(TodayInsightsB1QualityGateApproval{
		Version: TodayInsightsB1QualityGateVersion, CorpusHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Provider: testNarrativeSlotProviderID, Model: "test-slot-model", MaxOutputTokens: ai.DailyInsightMaxTokens,
		PromptRevision: identity.PromptRevision, ClaimPacketVersion: identity.ClaimPacketVersion,
		NarrativeVersion: identity.NarrativeVersion, ReviewFingerprint: identity.Fingerprint,
		ApprovedAt: "2026-09-17T12:00:00Z",
	}); err != nil {
		t.Fatalf("save B1 approval: %v", err)
	}
	if err := db.SaveSettings(map[string]string{SettingTodayInsightsB1Enabled: "true"}); err != nil {
		t.Fatalf("enable B1: %v", err)
	}
	config := AIConfig{Provider: testNarrativeSlotProviderID, Providers: map[string]AIProviderSettings{
		testNarrativeSlotProviderID: {APIKey: "test-key", Model: "test-slot-model"},
	}}
	snapshot := &health.DailyInsightSnapshot{
		Date: time.Now().In(db.reportTZLocation()).Format("2006-01-02"),
		Domains: []health.DailyInsightDomain{{
			Key: "sleep", DataState: "fresh", Confidence: "final",
			Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerConfirmedPersonal, ClaimID: "recent_sleep_below_reference", EvidenceIDs: []string{"sleep-evidence"}},
		}},
	}
	if !db.EnsureDailyInsightNarrativeSlotsAsync(snapshot, config, "en") {
		t.Fatal("eligible sleep meaning did not schedule a narrative worker")
	}
	entries, err := db.GetDailyInsightNarrativeSlots(snapshot.Date, "en")
	if err != nil {
		t.Fatalf("read lifecycle entries: %v", err)
	}
	if _, found := entries["sleep"]; !found {
		t.Fatalf("eligible sleep meaning did not allocate its lifecycle row: %#v", entries)
	}

	activeRecovery := &health.DailyInsightSnapshot{
		Date: snapshot.Date,
		Domains: []health.DailyInsightDomain{{
			Key: "energy", DataState: "fresh", Confidence: "final", NarrativeSubject: "active_recovery",
			Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerFactual, ClaimID: "energy_current_verdict_context", EvidenceIDs: []string{"energy-evidence"}},
		}},
	}
	if db.EnsureDailyInsightNarrativeSlotsAsync(activeRecovery, config, "en") {
		t.Fatal("energy action without a distinct meaning scheduled prose")
	}
	entries, err = db.GetDailyInsightNarrativeSlots(snapshot.Date, "en")
	if err != nil {
		t.Fatalf("read lifecycle entries after active-recovery check: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("active-recovery energy allocated lifecycle rows: %#v", entries)
	}
}

func TestTodayInsightsB1PreviewIsTenantLocalAndNotAnApproval(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	config := AIConfig{Provider: testNarrativeSlotProviderID, Providers: map[string]AIProviderSettings{
		testNarrativeSlotProviderID: {APIKey: "test-key", Model: "test-slot-model"},
	}}
	if err := db.SaveSettings(map[string]string{SettingTodayInsightsB1PreviewEnabled: "true"}); err != nil {
		t.Fatalf("enable B1 preview: %v", err)
	}
	if TodayInsightsB1Enabled(db) {
		t.Fatal("tenant preview must not set the approved B1 release flag")
	}
	if TodayInsightsB1ApprovedForConfig(db, config) {
		t.Fatal("tenant preview must not create a quality-gate approval")
	}
	if got := TodayInsightsB1NarrativeMode(db, config); got != TodayInsightsB1NarrativeModePreview {
		t.Fatalf("B1 narrative mode = %q, want preview", got)
	}
	if !TodayInsightsB1GenerationEnabled(db, config) {
		t.Fatal("tenant preview did not permit its own B1 generation")
	}
	if err := db.SaveSettings(map[string]string{SettingTodayInsightsB1PreviewEnabled: "false"}); err != nil {
		t.Fatalf("disable B1 preview: %v", err)
	}
	if got := TodayInsightsB1NarrativeMode(db, config); got != TodayInsightsB1NarrativeModeDisabled {
		t.Fatalf("B1 narrative mode after disable = %q, want disabled", got)
	}
}

func TestDailyInsightGenerationFingerprintIncludesStaticReviewIdentity(t *testing.T) {
	cfg := AIConfig{Provider: "openai", Providers: map[string]AIProviderSettings{
		"openai": {APIKey: "test-key", Model: "gpt-5.6-luna", ReasoningEffort: "none"},
	}}
	identity := ai.DailyInsightNarrativeSlotCurrentReviewIdentity()
	got := DailyInsightGenerationFingerprint(cfg, "en")
	want := ai.HashForGeneration("", ai.GenerationFingerprint{
		Provider: "openai", Model: "gpt-5.6-luna", ReasoningEffort: "none", MaxOutputTokens: ai.DailyInsightMaxTokens,
		PromptRevision: identity.PromptRevision + "|" + identity.Fingerprint + "|" + health.DailyInsightSnapshotVersion + "|" + health.DailyInsightPolicyVersion + "|" + health.DailyInsightActionCatalogVersion + "|en",
	})
	if got != want {
		t.Fatalf("generation fingerprint = %q, want identity-bound fingerprint %q", got, want)
	}
	if got == ai.HashForGeneration("", ai.GenerationFingerprint{
		Provider: "openai", Model: "gpt-5.6-luna", ReasoningEffort: "none", MaxOutputTokens: ai.DailyInsightMaxTokens,
		PromptRevision: health.DailyInsightPromptRevision + "|" + health.DailyInsightNarrativeInputVersion + "|" + health.DailyInsightNarrativeVersion + "|" + health.DailyInsightSnapshotVersion + "|" + health.DailyInsightPolicyVersion + "|" + health.DailyInsightActionCatalogVersion + "|en",
	}) {
		t.Fatal("generation fingerprint retained the legacy version-only prompt key")
	}
}

func TestDailyInsightNarrativeStaticRevisionMatchesReviewIdentity(t *testing.T) {
	identity := ai.DailyInsightNarrativeSlotCurrentReviewIdentity()
	if got, want := DailyInsightNarrativeStaticRevision(), identity.PromptRevision+"|"+identity.Fingerprint; got != want {
		t.Fatalf("stored static revision = %q, want %q", got, want)
	}
}
