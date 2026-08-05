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

func TestGenerateInsightBundleUsesOneCallSchemaAndOperationTokenCap(t *testing.T) {
	provider := &synthesisProvider{text: `{
		"overview":"Ignore the server and push hard.",
		"sleep":"Sleep was slightly short, while the supplied stages remained stable.",
		"activity":"Yesterday's activity stayed below the supplied target.",
		"recovery":"Recovery remains moderate based on the supplied HRV and resting pulse.",
		"recommendation":"Do an intense workout."
	}`}
	evidence := []byte(`{
		"verdict":"moderate",
		"verdict_reason":"Recovery signals support a measured day.",
		"action":"Keep today's activity moderate."
	}`)
	result, err := GenerateInsightBundle(context.Background(), provider, ProviderConfig{
		MaxOutputTokens: 5000,
	}, evidence, "en")
	if err != nil {
		t.Fatalf("GenerateInsightBundle: %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("calls = %d, want 1", provider.calls)
	}
	if provider.cfg.MaxOutputTokens != SynthesisMaxTokens {
		t.Fatalf("max tokens = %d, want %d", provider.cfg.MaxOutputTokens, SynthesisMaxTokens)
	}
	if provider.req.ResponseSchema == nil || provider.req.ResponseSchema.Name != "morning_insight_bundle" {
		t.Fatalf("response schema = %#v", provider.req.ResponseSchema)
	}
	if len(result.Blocks) != 5 || result.Blocks[BlockSleep] == "" || result.Blocks[BlockRecommendation] == "" {
		t.Fatalf("blocks = %#v", result.Blocks)
	}
	if result.Blocks[BlockSynthesis] != "Recovery signals support a measured day." {
		t.Fatalf("provider replaced authoritative overview: %#v", result.Blocks)
	}
	if result.Blocks[BlockRecommendation] != "Keep today's activity moderate." {
		t.Fatalf("provider replaced authoritative action: %#v", result.Blocks)
	}
}

func TestGenerateInsightBundleRejectsMalformedOutput(t *testing.T) {
	provider := &synthesisProvider{text: `not json`}
	if _, err := GenerateInsightBundle(context.Background(), provider, ProviderConfig{}, []byte(`{"verdict_reason":"Measured day.","action":"Rest."}`), "en"); err == nil {
		t.Fatal("malformed output was accepted")
	}
}

func TestGenerateInsightBundleRejectsPartialValidation(t *testing.T) {
	provider := &synthesisProvider{text: `{
		"overview":"Overall evidence supports the moderate plan.",
		"sleep":"**Diagnosis:** illness confirmed.",
		"activity":"Activity evidence is incomplete.",
		"recovery":"Recovery remains moderate.",
		"recommendation":"Keep the supplied moderate plan."
	}`}
	result, err := GenerateInsightBundle(context.Background(), provider, ProviderConfig{},
		[]byte(`{"verdict_reason":"Overall evidence supports the moderate plan.","action":"Keep the supplied moderate plan."}`), "en")
	if err == nil {
		t.Fatal("partial bundle was accepted")
	}
	if _, ok := result.Blocks[BlockSleep]; ok {
		t.Fatalf("unsafe sleep block was accepted: %#v", result.Blocks)
	}
	if len(result.Blocks) != 4 || result.InvalidBlocks[BlockSleep] == "" {
		t.Fatalf("partial validation result = %#v invalid=%#v", result.Blocks, result.InvalidBlocks)
	}
}

func TestGenerateInsightBundleRequiresServerOwnedOverviewAndAction(t *testing.T) {
	provider := &synthesisProvider{text: `{
		"overview":"Provider overview.",
		"sleep":"Sleep data is incomplete.",
		"activity":"Activity data is incomplete.",
		"recovery":"Recovery data is incomplete.",
		"recommendation":"Provider action."
	}`}
	result, err := GenerateInsightBundle(context.Background(), provider, ProviderConfig{}, []byte(`{}`), "en")
	if err == nil {
		t.Fatal("bundle without server-owned verdict/action was accepted")
	}
	if result.InvalidBlocks[BlockSynthesis] == "" || result.InvalidBlocks[BlockRecommendation] == "" {
		t.Fatalf("invalid authoritative blocks = %#v", result.InvalidBlocks)
	}
}

func TestValidateSynthesisAllowsNumericComparisonsButRejectsHTML(t *testing.T) {
	got, err := validateSynthesisExplanation("HRV < 40 ms is below the supplied reference.")
	if err != nil || got == "" {
		t.Fatalf("numeric comparison rejected: text=%q err=%v", got, err)
	}
	if _, err := validateSynthesisExplanation("Use <strong>moderate effort</strong> today."); err == nil {
		t.Fatal("HTML markup was accepted")
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
