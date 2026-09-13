package ui

import (
	"errors"
	"testing"

	"health-receiver/internal/ai"
	"health-receiver/internal/storage"
)

func TestTodayInsightsConfigRejectsB1EnableWithoutQualityGate(t *testing.T) {
	_, err := todayInsightsConfigSettings(map[string]bool{storage.SettingTodayInsightsB1Enabled: true}, false)
	if !errors.Is(err, errTodayInsightsB1QualityGateRequired) {
		t.Fatalf("error = %v, want quality-gate requirement", err)
	}
}

func TestTodayInsightsB1EvaluationMustMatchActiveConfiguration(t *testing.T) {
	active := storage.AIConfig{Provider: ai.ProviderGemini, Providers: map[string]storage.AIProviderSettings{
		ai.ProviderGemini: {APIKey: "configured", Model: "gemini-3-flash-preview", ReasoningEffort: "minimal"},
	}, MaxOutputTokens: ai.DailyInsightMaxTokens}
	evaluation := ai.DailyInsightNarrativeEvaluationOutput{
		Provider: ai.ProviderGemini, Model: "gemini-3-flash-preview", Reasoning: "minimal", MaxOutputTokens: ai.DailyInsightMaxTokens,
	}
	if _, err := todayInsightsB1EvaluationMatchesActiveConfig(active, evaluation, "minimal"); err != nil {
		t.Fatalf("matching evaluation rejected: %v", err)
	}
	evaluation.MaxOutputTokens--
	if _, err := todayInsightsB1EvaluationMatchesActiveConfig(active, evaluation, "minimal"); err == nil {
		t.Fatal("changed evaluation budget was accepted")
	}
}

func TestTodayInsightsConfigAllowsB1EnableAfterQualityGate(t *testing.T) {
	settings, err := todayInsightsConfigSettings(map[string]bool{storage.SettingTodayInsightsB1Enabled: true}, true)
	if err != nil {
		t.Fatalf("todayInsightsConfigSettings: %v", err)
	}
	if got := settings[storage.SettingTodayInsightsB1Enabled]; got != "true" {
		t.Fatalf("B1 setting = %q", got)
	}
}
