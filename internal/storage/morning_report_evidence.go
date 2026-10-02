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

func (s *DB) morningInsightEvidence(ctx context.Context, briefing *health.BriefingResponse, raw *health.RawMetrics, today, lang string) (health.MorningInsightEvidence, error) {
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
	if sleep.Capture != health.NightCaptureComplete || sleep.Assessment != health.NightDurationPlausible || sleep.Finalization != health.NightFinalFinal || sleep.BaselineNights < 7 || sleep.BaselineHours == nil || *sleep.BaselineHours <= 0 || *sleep.BaselineHours > 24 {
		sleep.BaselineHours = nil
		sleep.BaselineNights = 0
	}
	evidence := health.BuildMorningInsightEvidenceWithOptions(briefing, raw, health.MorningInsightOptions{ExcludeSections: map[string]bool{"sleep": true}})
	evidence.Date = today
	evidence.NightSleep = &sleep
	health.NormalizeMorningInsightEvidence(&evidence, lang)
	return evidence, nil
}

func morningBundleHash(evidence health.MorningInsightEvidence, briefing *health.BriefingResponse, fingerprint ai.GenerationFingerprint) string {
	hash := ai.HashForGeneration(ai.HashInsightBundle(evidence), fingerprint)
	if briefing.DailyDecision != nil {
		hash = PlanInputsHash(briefing.DailyDecision.ID, hash)
	}
	return hash
}

// MorningReportAIState reads the current night and admits only an exact,
// complete cached generation. Failure status belongs to that same input hash.
// It never calls a provider or writes data.
func (s *DB) MorningReportAIState(ctx context.Context, cfg AIConfig, lang, today string, briefing *health.BriefingResponse) (health.MorningReportSleep, map[string]string, bool, error) {
	fallback := health.MorningReportSleep{ReportDate: today, Capture: health.NightCaptureUnknown, Assessment: health.NightDurationUnknown}
	var raw *health.RawMetrics
	if cfg.Enabled() {
		raw = s.GetRawMetrics()
	}
	evidence, err := s.morningInsightEvidence(ctx, briefing, raw, today, lang)
	if err != nil {
		return fallback, nil, false, err
	}
	sleep := *evidence.NightSleep
	if !cfg.Enabled() {
		return sleep, nil, false, nil
	}
	if raw == nil {
		return sleep, nil, false, fmt.Errorf("morning report: raw metrics unavailable")
	}
	_, _, fingerprint, err := morningGenerationConfig(cfg)
	if err != nil {
		return sleep, nil, false, err
	}
	full := s.GetAIBlocksFull(today, lang)
	hash := morningBundleHash(evidence, briefing, fingerprint)
	if !aiBundleCacheComplete(full, hash) {
		return sleep, nil, s.morningAIRecentlyFailed(morningAIFailureKey(today, lang, hash)), nil
	}
	blocks := make(map[string]string, len(full))
	for _, key := range ai.GeneratedBlockOrder {
		blocks[key] = full[key].Text
	}
	return sleep, blocks, false, nil
}

// MorningReportEvidence preserves the sender's read-only cache contract.
func (s *DB) MorningReportEvidence(ctx context.Context, cfg AIConfig, lang, today string, briefing *health.BriefingResponse) (health.MorningReportSleep, map[string]string, error) {
	sleep, blocks, _, err := s.MorningReportAIState(ctx, cfg, lang, today, briefing)
	return sleep, blocks, err
}
