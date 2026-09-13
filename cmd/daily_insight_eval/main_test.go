package main

import (
	"testing"

	"health-receiver/internal/ai"
)

func TestEvaluatorRegistryDSNUsesIsolationRegistryWhenEnabled(t *testing.T) {
	values := map[string]string{
		"TENANT_DB_ISOLATION_ENABLED":     "true",
		"ADMIN_DATABASE_URL":              "postgres://admin@example/health",
		"REGISTRY_DATABASE_URL":           "postgres://registry@example/health",
		"TENANT_DATABASE_URL_BASE":        "postgres://example/health",
		"TENANT_DB_MASTER_SECRET":         "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"TENANT_DB_MASTER_SECRET_VERSION": "1",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	got, err := evaluatorRegistryDSN("postgres://tenant@example/health", lookup)
	if err != nil {
		t.Fatalf("evaluatorRegistryDSN() error = %v", err)
	}
	if want := values["REGISTRY_DATABASE_URL"]; got != want {
		t.Fatalf("registry dsn = %q, want %q", got, want)
	}
}

func TestEvaluatorRegistryDSNAllowsNoLegacyURLWhenIsolationIsEnabled(t *testing.T) {
	values := map[string]string{
		"TENANT_DB_ISOLATION_ENABLED":     "true",
		"ADMIN_DATABASE_URL":              "postgres://admin@example/health",
		"REGISTRY_DATABASE_URL":           "postgres://registry@example/health",
		"TENANT_DATABASE_URL_BASE":        "postgres://example/health",
		"TENANT_DB_MASTER_SECRET":         "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=",
		"TENANT_DB_MASTER_SECRET_VERSION": "1",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	got, err := evaluatorRegistryDSN("", lookup)
	if err != nil {
		t.Fatalf("evaluatorRegistryDSN() error = %v", err)
	}
	if want := values["REGISTRY_DATABASE_URL"]; got != want {
		t.Fatalf("registry dsn = %q, want %q", got, want)
	}
}

func TestEvaluatorRegistryDSNAllowsStandardPostgresEnvironment(t *testing.T) {
	values := map[string]string{
		"PGHOST":     "database.example",
		"PGPORT":     "5432",
		"PGDATABASE": "health",
		"PGUSER":     "readonly",
	}
	lookup := func(key string) (string, bool) { value, ok := values[key]; return value, ok }
	got, err := evaluatorRegistryDSN("", lookup)
	if err != nil {
		t.Fatalf("evaluatorRegistryDSN() error = %v", err)
	}
	if got != "" {
		t.Fatalf("registry dsn = %q, want empty PG* resolved DSN", got)
	}
}

func TestGlobalAIConfigUsesInstallationWideProviderSettings(t *testing.T) {
	config := globalAIConfig(map[string]string{
		"ai_provider":                   ai.ProviderOpenAI,
		"openai_api_key":                "configured-openai-key",
		"openai_model":                  "gpt-5.6-luna",
		"openai_reasoning_effort":       "medium",
		"gemini_api_key":                "configured-gemini-key",
		"gemini_model":                  "gemini-2.5-flash",
		"gemini_reasoning_effort":       "low",
		"unregistered_provider_api_key": "must-not-be-imported",
	})

	if config.Provider != ai.ProviderOpenAI {
		t.Fatalf("provider = %q, want %q", config.Provider, ai.ProviderOpenAI)
	}
	if got := config.SettingsFor(ai.ProviderOpenAI); got.APIKey != "configured-openai-key" || got.Model != "gpt-5.6-luna" || got.ReasoningEffort != "medium" {
		t.Fatalf("openai settings = %#v", got)
	}
	if got := config.SettingsFor(ai.ProviderGemini); got.APIKey != "configured-gemini-key" || got.Model != "gemini-2.5-flash" || got.ReasoningEffort != "low" {
		t.Fatalf("gemini settings = %#v", got)
	}
	if _, exists := config.Providers["unregistered_provider"]; exists {
		t.Fatal("unregistered provider was imported into evaluator defaults")
	}
}
