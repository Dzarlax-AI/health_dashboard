package ai

import (
	"context"
	"errors"
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
		{"prompt revision", func(v *GenerationFingerprint) { v.PromptRevision = "health-briefing-v2" }},
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

type cancellationProvider struct{}

func (cancellationProvider) Descriptor() ProviderDescriptor {
	return ProviderDescriptor{ID: "cancellation"}
}

func (cancellationProvider) ListModels(context.Context, string) ([]Model, error) {
	return nil, nil
}

func (cancellationProvider) Generate(ctx context.Context, _ ProviderConfig, _ GenerationRequest) (GenerationResult, error) {
	return GenerationResult{}, ctx.Err()
}

func TestBlockGenerationPropagatesCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := GenerateLeafBlocks(ctx, cancellationProvider{}, ProviderConfig{}, nil, "en", nil)
	if len(results) != len(LeafBlocks) {
		t.Fatalf("results = %d, want %d", len(results), len(LeafBlocks))
	}
	for _, result := range results {
		if !errors.Is(result.Err, context.Canceled) {
			t.Fatalf("%s error = %v, want context canceled", result.Block, result.Err)
		}
	}
	if _, err := GenerateRecommendation(
		ctx, cancellationProvider{}, ProviderConfig{}, nil, "en",
		"", "", "", nil, nil, InsightContext{},
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("recommendation error = %v, want context canceled", err)
	}
}
