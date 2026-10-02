package ai

import (
	"context"
	"encoding/json"
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

func TestGenerateInsightBundleConstrainsPreliminarySleepAndRemovesBaseline(t *testing.T) {
	provider := &synthesisProvider{text: `{"overview":"Server verdict.","sleep":"awaiting_completion","activity":"activity_2","recovery":"recovery_2","recommendation":"Keep the server plan."}`}
	evidence := []byte(`{"date":"2026-09-30","verdict_reason":"Server verdict.","action":"Keep the server plan.","sections":[{"key":"activity","summary":"Activity remained steady yesterday.","details":[{"label":"Steps","note":"within the recent range"}]},{"key":"recovery","summary":"Recovery signals are mixed.","details":[{"label":"HRV","note":"close to its recent range"}]}],"daily":[{"date":"2026-09-30","hrv":50,"sleep":7.5,"deep":2,"rem":1.5}],"night_sleep":{"report_date":"2026-09-30","date":"2026-09-30","hours":7.5,"baseline_hours":8.1,"baseline_nights":12,"capture":"partial","assessment":"plausible","finalization":"provisional"}}`)
	result, err := GenerateInsightBundle(context.Background(), provider, ProviderConfig{}, evidence, "ru")
	if err != nil {
		t.Fatalf("GenerateInsightBundle: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(provider.req.UserPayload, &payload); err != nil {
		t.Fatalf("decode provider payload: %v", err)
	}
	night := payload["night_sleep"].(map[string]any)
	if _, ok := night["baseline_hours"]; ok {
		t.Fatalf("partial provider payload includes baseline_hours: %#v", night)
	}
	if _, ok := night["baseline_nights"]; ok {
		t.Fatalf("partial provider payload includes baseline_nights: %#v", night)
	}
	daily := payload["daily"].([]any)[0].(map[string]any)
	if _, ok := daily["sleep"]; ok {
		t.Fatalf("partial provider payload includes daily sleep: %#v", daily)
	}
	if _, ok := daily["deep"]; ok {
		t.Fatalf("partial provider payload includes daily deep sleep: %#v", daily)
	}
	options := payload["preliminary_explanations"].(map[string]any)
	activityOptions := options["activity"].([]any)
	recoveryOptions := options["recovery"].([]any)
	if activityOptions[1].(map[string]any)["text"] != "Steps: within the recent range" || recoveryOptions[1].(map[string]any)["text"] != "HRV: close to its recent range" {
		t.Fatalf("provider options not derived from server sections: %#v", options)
	}
	property := provider.req.ResponseSchema.Schema["properties"].(map[string]any)["sleep"].(map[string]any)
	if got := property["enum"].([]string); len(got) != 2 || got[0] != health.MorningSleepChoiceRecordedDuration || got[1] != health.MorningSleepChoiceAwaitingCompletion {
		t.Fatalf("preliminary schema enum = %#v", property["enum"])
	}
	if result.Blocks[BlockSleep] != "Из-за неполной записи по одной длительности нельзя уверенно судить о недосыпе." {
		t.Fatalf("server localized choice = %q", result.Blocks[BlockSleep])
	}
	if result.Blocks[BlockYesterday] != "Steps: within the recent range" || result.Blocks[BlockRecovery] != "HRV: close to its recent range" {
		t.Fatalf("server option mapping changed selected facts: yesterday=%q recovery=%q", result.Blocks[BlockYesterday], result.Blocks[BlockRecovery])
	}
}

func TestGenerateInsightBundleRejectsUnsupportedPreliminarySleepText(t *testing.T) {
	provider := &synthesisProvider{text: `{"overview":"Server verdict.","sleep":"The short night caused poor recovery.","activity":"The short night reduced your activity.","recovery":"Your short sleep caused poor recovery.","recommendation":"Keep the server plan."}`}
	evidence := []byte(`{"date":"2026-09-30","verdict_reason":"Server verdict.","action":"Keep the server plan.","night_sleep":{"report_date":"2026-09-30","date":"2026-09-30","hours":4,"capture":"partial","assessment":"plausible","finalization":"provisional"}}`)
	result, err := GenerateInsightBundle(context.Background(), provider, ProviderConfig{}, evidence, "en")
	if err == nil {
		t.Fatal("unsupported preliminary sleep claim accepted")
	}
	if len(result.Blocks) != 0 || result.InvalidBlocks[BlockSleep] == "" || result.InvalidBlocks[BlockYesterday] == "" || result.InvalidBlocks[BlockRecovery] == "" {
		t.Fatalf("unsafe generation was not rejected as a bundle: blocks=%#v invalid=%#v", result.Blocks, result.InvalidBlocks)
	}
}

func TestGenerateInsightBundleConstrainsFinalizedPartialSleep(t *testing.T) {
	provider := &synthesisProvider{text: `{"overview":"Server verdict.","sleep":"awaiting_completion","activity":"activity_1","recovery":"recovery_1","recommendation":"Keep the server plan."}`}
	evidence := []byte(`{"date":"2026-09-30","verdict_reason":"Server verdict.","action":"Keep the server plan.","night_sleep":{"report_date":"2026-09-30","date":"2026-09-30","hours":7.5,"capture":"partial","assessment":"plausible","finalization":"final"}}`)
	result, err := GenerateInsightBundle(context.Background(), provider, ProviderConfig{}, evidence, "sr")
	if err != nil {
		t.Fatalf("GenerateInsightBundle: %v", err)
	}
	property := provider.req.ResponseSchema.Schema["properties"].(map[string]any)["sleep"].(map[string]any)
	if got := property["enum"].([]string); len(got) != 2 || got[1] != health.MorningSleepChoiceAwaitingCompletion {
		t.Fatalf("partial final schema enum = %#v", property["enum"])
	}
	if result.Blocks[BlockSleep] != "Pošto je zapis nepotpun, samo trajanje ne može pouzdano da pokaže da li je sna bilo premalo." {
		t.Fatalf("localized finalized-partial explanation = %q", result.Blocks[BlockSleep])
	}
}

func TestGenerateInsightBundleMapsPreliminarySectionChoicesInAllLocales(t *testing.T) {
	locales := []struct {
		lang, activity, recovery string
	}{
		{"en", "Yesterday activity was steady.", "Recovery signals were mixed."},
		{"ru", "Вчерашняя активность была стабильной.", "Сигналы восстановления были неоднозначными."},
		{"sr", "Juče je aktivnost bila stabilna.", "Signali oporavka bili su mešoviti."},
	}
	for _, locale := range locales {
		t.Run(locale.lang, func(t *testing.T) {
			provider := &synthesisProvider{text: `{"overview":"Server verdict.","sleep":"recorded_duration","activity":"activity_1","recovery":"recovery_1","recommendation":"Keep the server plan."}`}
			body, err := json.Marshal(health.MorningInsightEvidence{
				Date: "2026-09-30", VerdictReason: "Server verdict.", Action: "Keep the server plan.",
				NightSleep: &health.MorningReportSleep{ReportDate: "2026-09-30", Date: "2026-09-30", Hours: floatPtr(7.5), Capture: health.NightCapturePartial, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalProvisional},
				Sections:   []health.MorningInsightSection{{Key: "activity", Summary: locale.activity}, {Key: "recovery", Summary: locale.recovery}},
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := GenerateInsightBundle(context.Background(), provider, ProviderConfig{}, body, locale.lang)
			if err != nil {
				t.Fatalf("GenerateInsightBundle: %v", err)
			}
			if result.Blocks[BlockYesterday] != locale.activity || result.Blocks[BlockRecovery] != locale.recovery {
				t.Fatalf("localized choice mapping: yesterday=%q recovery=%q", result.Blocks[BlockYesterday], result.Blocks[BlockRecovery])
			}
			if _, err := validateInsightText(result.Blocks[BlockYesterday]); err != nil {
				t.Fatalf("localized activity option failed text validation: %v", err)
			}
		})
	}
}

func TestGenerateInsightBundleKeepsFinalSleepFreeText(t *testing.T) {
	provider := &synthesisProvider{text: `{"overview":"Server verdict.","sleep":"The supplied stages were consistent with the recorded duration.","activity":"Activity was recorded yesterday.","recovery":"Recovery remains within the supplied range.","recommendation":"Keep the server plan."}`}
	evidence := []byte(`{"date":"2026-09-30","verdict_reason":"Server verdict.","action":"Keep the server plan.","night_sleep":{"report_date":"2026-09-30","date":"2026-09-30","hours":7.5,"baseline_hours":8.1,"baseline_nights":12,"capture":"complete","assessment":"plausible","finalization":"final"}}`)
	result, err := GenerateInsightBundle(context.Background(), provider, ProviderConfig{}, evidence, "en")
	if err != nil {
		t.Fatalf("GenerateInsightBundle: %v", err)
	}
	if result.Blocks[BlockSleep] != "The supplied stages were consistent with the recorded duration." {
		t.Fatalf("final sleep text changed: %#v", result.Blocks)
	}
	property := provider.req.ResponseSchema.Schema["properties"].(map[string]any)["sleep"].(map[string]any)
	if _, ok := property["enum"]; ok {
		t.Fatalf("final sleep schema unexpectedly constrained: %#v", property)
	}
	var payload map[string]any
	if err := json.Unmarshal(provider.req.UserPayload, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["night_sleep"].(map[string]any)["baseline_hours"]; !ok {
		t.Fatalf("eligible final baseline omitted: %#v", payload["night_sleep"])
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
