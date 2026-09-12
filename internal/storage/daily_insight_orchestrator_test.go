package storage

import (
	"context"
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

func TestEnsureDailyInsightNarrativeAsyncRequiresB1QualityGate(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	if err := db.SaveSettings(map[string]string{SettingTodayInsightsB1Enabled: "true"}); err != nil {
		t.Fatalf("set bare B1 flag: %v", err)
	}
	snapshot := &health.DailyInsightSnapshot{Domains: []health.DailyInsightDomain{{
		Key: "sleep", DataState: "fresh", Confidence: "final",
		Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerConfirmedPersonal, ClaimID: "recent_sleep_below_reference", EvidenceIDs: []string{"sleep-evidence"}},
	}}}
	config := AIConfig{Provider: "openai", Providers: map[string]AIProviderSettings{
		"openai": {APIKey: "test-key", Model: "gpt-5.6-luna", ReasoningEffort: "none"},
	}}
	if db.EnsureDailyInsightNarrativeAsync(snapshot, config, "en") {
		t.Fatal("bare B1 flag scheduled a provider generation without a quality gate")
	}
}

func TestEnsureDailyInsightNarrativeDirectlyRequiresB1QualityGate(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	snapshot := &health.DailyInsightSnapshot{
		Date: time.Now().In(db.reportTZLocation()).Format("2006-01-02"),
		Domains: []health.DailyInsightDomain{{
			Key: "sleep", DataState: "fresh", Confidence: "final",
			Insight: health.DailyInsight{State: "insight", AnswerKind: health.DailyInsightAnswerConfirmedPersonal, ClaimID: "recent_sleep_below_reference", EvidenceIDs: []string{"sleep-evidence"}},
		}},
	}
	// If this guard regresses, provider resolution reaches the deliberately
	// unknown provider and the test fails before any network-capable adapter.
	config := AIConfig{Provider: "unreviewed-provider", Providers: map[string]AIProviderSettings{
		"unreviewed-provider": {APIKey: "test-key", Model: "test-model", ReasoningEffort: "none"},
	}}
	if err := db.EnsureDailyInsightNarrative(context.Background(), snapshot, config, "en", "material-hash", "provider-fingerprint"); err != nil {
		t.Fatalf("direct call bypassed B1 gate: %v", err)
	}
}

func TestDailyInsightGenerationFingerprintIncludesStaticReviewIdentity(t *testing.T) {
	cfg := AIConfig{Provider: "openai", Providers: map[string]AIProviderSettings{
		"openai": {APIKey: "test-key", Model: "gpt-5.6-luna", ReasoningEffort: "none"},
	}}
	identity := ai.DailyInsightNarrativeCurrentReviewIdentity()
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
	identity := ai.DailyInsightNarrativeCurrentReviewIdentity()
	if got, want := DailyInsightNarrativeStaticRevision(), identity.PromptRevision+"|"+identity.Fingerprint; got != want {
		t.Fatalf("stored static revision = %q, want %q", got, want)
	}
}
