package storage

import (
	"context"
	"fmt"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

// morningGenerationConfig is shared by generation and cache validation so
// default model, reasoning, output limits, and prompt revisions cannot drift.
func morningGenerationConfig(cfg AIConfig) (ai.Provider, ai.ProviderConfig, ai.GenerationFingerprint, error) {
	provider, err := ai.GetProvider(cfg.Provider)
	if err != nil {
		return nil, ai.ProviderConfig{}, ai.GenerationFingerprint{}, err
	}
	active := cfg.ActiveSettings()
	descriptor := provider.Descriptor()
	if active.Model == "" {
		active.Model = descriptor.DefaultModel
	}
	if active.ReasoningEffort == "" {
		active.ReasoningEffort = descriptor.DefaultReasoning
	}
	limit := cfg.MaxOutputTokens
	if limit <= 0 || limit > ai.SynthesisMaxTokens {
		limit = ai.SynthesisMaxTokens
	}
	return provider, ai.ProviderConfig{APIKey: active.APIKey, Model: active.Model, ReasoningEffort: active.ReasoningEffort, MaxOutputTokens: limit}, ai.GenerationFingerprint{Provider: cfg.Provider, Model: active.Model, ReasoningEffort: active.ReasoningEffort, MaxOutputTokens: limit, PromptRevision: ai.PromptRevision}, nil
}

func (s *DB) morningInsightEvidence(ctx context.Context, briefing *health.BriefingResponse, raw *health.RawMetrics, today string) (health.MorningInsightEvidence, error) {
	date, err := time.Parse("2006-01-02", today)
	if err != nil {
		return health.MorningInsightEvidence{}, err
	}
	nights, err := s.ListCompletedNightSleep(ctx, date.AddDate(0, 0, -60).Format("2006-01-02"), today)
	if err != nil {
		return health.MorningInsightEvidence{}, err
	}
	sleep := health.SelectMorningReportSleep(today, nights)
	// Legacy ingestion may lack canonical coverage. Keep the observed date and
	// duration but never claim complete capture or use its cached AI narrative.
	if briefing.Sleep != nil && briefing.Sleep.LatestDate > sleep.Date && briefing.Sleep.LatestDate <= today && briefing.Sleep.LatestTotal != nil && *briefing.Sleep.LatestTotal > 0 {
		hours := *briefing.Sleep.LatestTotal
		sleep = health.MorningReportSleep{ReportDate: today, Date: briefing.Sleep.LatestDate, Hours: &hours, Capture: health.NightCaptureUnknown, Assessment: health.NightDurationUnknown}
	}
	evidence := health.BuildMorningInsightEvidenceWithOptions(briefing, raw, health.MorningInsightOptions{ExcludeSections: map[string]bool{"sleep": true}})
	evidence.Date = today
	evidence.NightSleep = &sleep
	return evidence, nil
}

func morningBundleHash(evidence health.MorningInsightEvidence, briefing *health.BriefingResponse, fingerprint ai.GenerationFingerprint) string {
	hash := ai.HashForGeneration(ai.HashInsightBundle(evidence), fingerprint)
	if briefing.DailyDecision != nil {
		hash = PlanInputsHash(briefing.DailyDecision.ID, hash)
	}
	return hash
}

// MorningReportEvidence reads the current night and admits only an exact,
// complete cached generation. It never calls a provider or writes data.
func (s *DB) MorningReportEvidence(ctx context.Context, cfg AIConfig, lang, today string, briefing *health.BriefingResponse) (health.MorningReportSleep, map[string]string, error) {
	fallback := health.MorningReportSleep{ReportDate: today, Capture: health.NightCaptureUnknown, Assessment: health.NightDurationUnknown}
	var raw *health.RawMetrics
	if cfg.Enabled() {
		raw = s.GetRawMetrics()
	}
	evidence, err := s.morningInsightEvidence(ctx, briefing, raw, today)
	if err != nil {
		return fallback, nil, err
	}
	sleep := *evidence.NightSleep
	if !cfg.Enabled() {
		return sleep, nil, nil
	}
	if raw == nil {
		return sleep, nil, fmt.Errorf("morning report: raw metrics unavailable")
	}
	_, _, fingerprint, err := morningGenerationConfig(cfg)
	if err != nil {
		return sleep, nil, err
	}
	full := s.GetAIBlocksFull(today, lang)
	if !aiBundleCacheComplete(full, morningBundleHash(evidence, briefing, fingerprint)) {
		return sleep, nil, nil
	}
	blocks := make(map[string]string, len(full))
	for _, key := range ai.GeneratedBlockOrder {
		blocks[key] = full[key].Text
	}
	return sleep, blocks, nil
}
