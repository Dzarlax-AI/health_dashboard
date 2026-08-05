package ai

import (
	"testing"
)

func TestHashForGenerationIncludesOutputAffectingConfiguration(t *testing.T) {
	base := GenerationFingerprint{
		Provider:        ProviderGemini,
		Model:           "gemini-2.5-flash",
		ReasoningEffort: "",
		MaxOutputTokens: 5000,
		PromptRevision:  PromptRevision,
	}
	baseHash := HashForGeneration("health-inputs", base)
	if got := HashForGeneration("health-inputs", base); got != baseHash {
		t.Fatalf("same fingerprint produced different hashes: %q != %q", got, baseHash)
	}

	tests := []struct {
		name   string
		change func(*GenerationFingerprint)
	}{
		{"provider", func(v *GenerationFingerprint) { v.Provider = ProviderOpenAI }},
		{"model", func(v *GenerationFingerprint) { v.Model = "gpt-5.6-luna" }},
		{"reasoning", func(v *GenerationFingerprint) { v.ReasoningEffort = "low" }},
		{"max output tokens", func(v *GenerationFingerprint) { v.MaxOutputTokens = 6000 }},
		{"prompt revision", func(v *GenerationFingerprint) { v.PromptRevision = "health-briefing-v3" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := base
			tt.change(&changed)
			if got := HashForGeneration("health-inputs", changed); got == baseHash {
				t.Fatalf("%s change did not invalidate hash", tt.name)
			}
		})
	}
}
