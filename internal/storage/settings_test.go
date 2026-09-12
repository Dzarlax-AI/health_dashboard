package storage

import (
	"strings"
	"testing"

	"health-receiver/internal/ai"
)

func TestValidateTodayInsightsB1QualityGateApproval(t *testing.T) {
	identity := ai.DailyInsightNarrativeCurrentReviewIdentity()
	valid := TodayInsightsB1QualityGateApproval{
		Version: TodayInsightsB1QualityGateVersion, CorpusHash: strings.Repeat("a", 64),
		Provider: "openai", Model: "gpt-5.6-luna", Reasoning: "none",
		PromptRevision: identity.PromptRevision, ClaimPacketVersion: identity.ClaimPacketVersion,
		NarrativeVersion: identity.NarrativeVersion, ReviewFingerprint: identity.Fingerprint,
		ApprovedAt: "2026-09-12T10:00:00Z",
	}
	if err := ValidateTodayInsightsB1QualityGateApproval(valid); err != nil {
		t.Fatalf("valid approval rejected: %v", err)
	}
	valid.CorpusHash = strings.Repeat("A", 64)
	if err := ValidateTodayInsightsB1QualityGateApproval(valid); err == nil || !strings.Contains(err.Error(), "lowercase") {
		t.Fatalf("uppercase hash error = %v", err)
	}
}

func TestTodayInsightsB1QualityGateMatchesOnlyReviewedModelConfig(t *testing.T) {
	identity := ai.DailyInsightNarrativeCurrentReviewIdentity()
	approval := TodayInsightsB1QualityGateApproval{
		Version: TodayInsightsB1QualityGateVersion, CorpusHash: strings.Repeat("b", 64),
		Provider: "openai", Model: "gpt-5.6-luna", Reasoning: "none",
		PromptRevision: identity.PromptRevision, ClaimPacketVersion: identity.ClaimPacketVersion,
		NarrativeVersion: identity.NarrativeVersion, ReviewFingerprint: identity.Fingerprint,
		ApprovedAt: "2026-09-12T10:00:00Z",
	}
	cfg := AIConfig{Provider: "openai", Providers: map[string]AIProviderSettings{
		"openai": {APIKey: "not-empty", Model: "gpt-5.6-luna", ReasoningEffort: "none"},
	}}
	if !TodayInsightsB1QualityGateMatchesConfig(approval, cfg) {
		t.Fatal("matching reviewed AI configuration was rejected")
	}
	cfg.Providers["openai"] = AIProviderSettings{APIKey: "not-empty", Model: "gpt-5.6-terra", ReasoningEffort: "none"}
	if TodayInsightsB1QualityGateMatchesConfig(approval, cfg) {
		t.Fatal("changed model reused an old B1 quality-gate approval")
	}
	cfg.Providers["openai"] = AIProviderSettings{APIKey: "not-empty", Model: "gpt-5.6-luna", ReasoningEffort: "none"}
	cfg.Providers["openai"] = AIProviderSettings{APIKey: "not-empty", Model: "gpt-5.6-luna", ReasoningEffort: "low"}
	if TodayInsightsB1QualityGateMatchesConfig(approval, cfg) {
		t.Fatal("changed reasoning reused an old B1 quality-gate approval")
	}
	cfg.Providers["openai"] = AIProviderSettings{APIKey: "not-empty", Model: "gpt-5.6-luna", ReasoningEffort: "none"}
	cfg.Provider = "gemini"
	cfg.Providers["gemini"] = AIProviderSettings{APIKey: "not-empty", Model: "gpt-5.6-luna", ReasoningEffort: "none"}
	if TodayInsightsB1QualityGateMatchesConfig(approval, cfg) {
		t.Fatal("changed provider reused an old B1 quality-gate approval")
	}
	cfg.Provider = "openai"
	cfg.Providers["openai"] = AIProviderSettings{Model: "gpt-5.6-luna", ReasoningEffort: "none"}
	if TodayInsightsB1QualityGateMatchesConfig(approval, cfg) {
		t.Fatal("missing active API key reused an old B1 quality-gate approval")
	}
	cfg.Providers["openai"] = AIProviderSettings{APIKey: "not-empty", Model: "gpt-5.6-luna", ReasoningEffort: "none"}
	approval.ReviewFingerprint = strings.Repeat("c", 64)
	if TodayInsightsB1QualityGateMatchesConfig(approval, cfg) {
		t.Fatal("changed prompt/schema fingerprint reused an old B1 quality-gate approval")
	}
}
