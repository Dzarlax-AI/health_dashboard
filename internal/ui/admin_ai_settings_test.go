package ui

import (
	"encoding/json"
	"strings"
	"testing"

	"health-receiver/internal/ai"
	"health-receiver/internal/storage"
)

func TestBuildAdminAISettingsUpdatePreservesOrClearsKeyExplicitly(t *testing.T) {
	base := adminAISettingsRequest{
		Provider:        ai.ProviderOpenAI,
		Model:           "gpt-5.6-luna",
		ReasoningEffort: "none",
		MaxOutputTokens: 5000,
	}
	update, err := buildAdminAISettingsUpdate(base)
	if err != nil {
		t.Fatalf("build update: %v", err)
	}
	if _, ok := update["openai_api_key"]; ok {
		t.Fatal("blank API key must preserve the stored key")
	}

	base.ClearAPIKey = true
	update, err = buildAdminAISettingsUpdate(base)
	if err != nil {
		t.Fatalf("build clear update: %v", err)
	}
	if value, ok := update["openai_api_key"]; !ok || value != "" {
		t.Fatalf("explicit clear = %q, present=%v", value, ok)
	}
}

func TestBuildAdminAISettingsUpdateRejectsUnknownProvider(t *testing.T) {
	_, err := buildAdminAISettingsUpdate(adminAISettingsRequest{
		Provider: "missing", MaxOutputTokens: 5000,
	})
	if err == nil {
		t.Fatal("unknown provider returned no error")
	}
}

func TestAdminAISettingsPayloadNeverReturnsAPIKeys(t *testing.T) {
	cfg := storage.AIConfig{
		Provider: ai.ProviderOpenAI,
		Providers: map[string]storage.AIProviderSettings{
			ai.ProviderGemini: {APIKey: "gemini-secret", Model: "gemini-2.5-flash"},
			ai.ProviderOpenAI: {APIKey: "openai-secret", Model: "gpt-5.6-luna", ReasoningEffort: "none"},
		},
		MaxOutputTokens: 5000,
	}
	body, err := json.Marshal(adminAISettingsPayload(cfg))
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	text := string(body)
	for _, secret := range []string{"gemini-secret", "openai-secret", `"api_key":`} {
		if strings.Contains(text, secret) {
			t.Fatalf("payload leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, `"configured":true`) {
		t.Fatalf("payload did not retain configured status: %s", text)
	}
}
