package ai

import (
	"context"
	"testing"

	"health-receiver/internal/health"
)

type synthesisProvider struct {
	calls int
	req   GenerationRequest
	cfg   ProviderConfig
	text  string
}

func (p *synthesisProvider) Descriptor() ProviderDescriptor {
	return ProviderDescriptor{ID: "synthesis"}
}

func (p *synthesisProvider) ListModels(context.Context, string) ([]Model, error) {
	return nil, nil
}

func (p *synthesisProvider) Generate(_ context.Context, cfg ProviderConfig, req GenerationRequest) (GenerationResult, error) {
	p.calls++
	p.cfg = cfg
	p.req = req
	return GenerationResult{Text: p.text}, nil
}

func TestGenerateSynthesisUsesSchemaAndOperationTokenCap(t *testing.T) {
	provider := &synthesisProvider{text: `{"explanation":"HRV is above your usual level, while sleep quality keeps the server plan moderate."}`}
	result, err := GenerateSynthesis(context.Background(), provider, ProviderConfig{
		MaxOutputTokens: 5000,
	}, []byte(`{"verdict":"moderate"}`), "en")
	if err != nil {
		t.Fatalf("GenerateSynthesis: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("calls = %d, want 1", provider.calls)
	}
	if provider.cfg.MaxOutputTokens != SynthesisMaxTokens {
		t.Fatalf("max tokens = %d, want %d", provider.cfg.MaxOutputTokens, SynthesisMaxTokens)
	}
	if provider.req.ResponseSchema == nil || provider.req.ResponseSchema.Name != "morning_insight_synthesis" {
		t.Fatalf("response schema = %#v", provider.req.ResponseSchema)
	}
	if result.Text != "HRV is above your usual level, while sleep quality keeps the server plan moderate." {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestGenerateSynthesisRejectsUnsafeOrMalformedOutput(t *testing.T) {
	for _, text := range []string{
		`not json`,
		`{"explanation":""}`,
		`{"explanation":"**Diagnosis:** illness confirmed."}`,
	} {
		provider := &synthesisProvider{text: text}
		if _, err := GenerateSynthesis(context.Background(), provider, ProviderConfig{}, []byte(`{}`), "en"); err == nil {
			t.Fatalf("output %q was accepted", text)
		}
	}
}

func TestHashSynthesisUsesExactDatedEvidence(t *testing.T) {
	base := health.MorningInsightEvidence{
		Date:    "2026-08-04",
		Verdict: "moderate",
		Daily: []health.DailyHealthMetrics{
			{Date: "2026-08-04", HRV: floatPtr(51.8)},
			{Date: "2026-08-03", HRV: floatPtr(38)},
		},
	}
	changed := base
	changed.Daily = append([]health.DailyHealthMetrics(nil), base.Daily...)
	changed.Daily[0].Date = "2026-08-03"
	if HashSynthesis(base) == HashSynthesis(changed) {
		t.Fatal("changing the metric date did not invalidate synthesis hash")
	}
}

func floatPtr(v float64) *float64 { return &v }
