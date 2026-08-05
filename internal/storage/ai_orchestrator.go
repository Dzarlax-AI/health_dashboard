package storage

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

// aiRegenFailBackoff is how long we wait after a failed regen before
// retrying. Keeps a sustained upstream outage from amplifying into one
// regen attempt per polling tick.
const aiRegenFailBackoff = 5 * time.Minute

// EnsureTodayAIInsight generates one structured five-block briefing from a
// deterministic, date-aligned evidence packet. The server owns the verdict,
// reasons, sections, and action; the provider may only explain them. Returns
// the canonical overview string, or "" if AI is disabled / no metrics exist.
//
// Safe to call repeatedly — a cached row whose inputs_hash matches the exact
// evidence plus generation fingerprint skips the provider call.
//
// Concurrency: only one EnsureTodayAIInsight per (date, lang) runs at a
// time across the process. Concurrent calls (sync morning-retry vs async
// poller-driven regen) return the current cache instead of duplicating
// provider work. After a failure, retries are throttled to once per
// aiRegenFailBackoff so an upstream outage doesn't compound.
func (s *DB) EnsureTodayAIInsight(aiCfg AIConfig, lang string) string {
	return s.EnsureTodayAIInsightContext(context.Background(), aiCfg, lang)
}

// EnsureTodayAIInsightContext is the cancellation-aware variant used by
// schedulers and shutdown-aware callers. AI insight v3 makes exactly one
// provider call for one date-aligned evidence packet and stores five aligned
// rows under their existing compatibility keys.
func (s *DB) EnsureTodayAIInsightContext(ctx context.Context, aiCfg AIConfig, lang string) string {
	if !aiCfg.Enabled() {
		return ""
	}
	provider, err := ai.GetProvider(aiCfg.Provider)
	if err != nil {
		log.Printf("EnsureTodayAIInsight: %v", err)
		return ""
	}
	active := aiCfg.ActiveSettings()
	descriptor := provider.Descriptor()
	if active.Model == "" {
		active.Model = descriptor.DefaultModel
	}
	if active.ReasoningEffort == "" {
		active.ReasoningEffort = descriptor.DefaultReasoning
	}
	maxOutputTokens := aiCfg.MaxOutputTokens
	if maxOutputTokens <= 0 || maxOutputTokens > ai.SynthesisMaxTokens {
		maxOutputTokens = ai.SynthesisMaxTokens
	}
	providerCfg := ai.ProviderConfig{
		APIKey:          active.APIKey,
		Model:           active.Model,
		MaxOutputTokens: maxOutputTokens,
		ReasoningEffort: active.ReasoningEffort,
	}
	fingerprint := ai.GenerationFingerprint{
		Provider:        aiCfg.Provider,
		Model:           active.Model,
		ReasoningEffort: active.ReasoningEffort,
		MaxOutputTokens: maxOutputTokens,
		PromptRevision:  ai.PromptRevision,
	}
	today := time.Now().In(s.reportTZLocation()).Format("2006-01-02")
	key := today + "|" + lang
	failureKey := key + "|" + ai.HashForGeneration("", fingerprint)

	if _, loaded := s.aiRegenInFlight.LoadOrStore(key, true); loaded {
		return s.GetAIInsightCombined(today, lang)
	}
	defer s.aiRegenInFlight.Delete(key)

	if v, ok := s.aiRegenLastFailAt.Load(failureKey); ok {
		if t, ok := v.(time.Time); ok && time.Since(t) < aiRegenFailBackoff {
			return s.GetAIInsightCombined(today, lang)
		}
	}

	raw := s.GetRawMetrics()
	if raw == nil {
		log.Println("EnsureTodayAIInsight: no raw metrics available")
		return ""
	}
	briefing, err := s.GetHealthBriefing(lang)
	if err != nil {
		log.Printf("EnsureTodayAIInsight: briefing: %v", err)
		return ""
	}
	evidence := health.BuildMorningInsightEvidence(briefing, raw)
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		log.Printf("EnsureTodayAIInsight: marshal evidence: %v", err)
		return ""
	}
	bundleHash := ai.HashForGeneration(ai.HashInsightBundle(evidence), fingerprint)
	if aiBundleCacheComplete(s.GetAIBlocksFull(today, lang), bundleHash) {
		s.aiRegenLastFailAt.Delete(failureKey)
		return s.GetAIInsightCombined(today, lang)
	}

	generated, err := ai.GenerateInsightBundle(ctx, provider, providerCfg, evidenceJSON, lang)
	log.Printf(
		"EnsureTodayAIInsight: provider=%s model=%s block=BUNDLE request_id=%q attempts=%d latency=%s input_tokens=%d output_tokens=%d total_tokens=%d finish=%q",
		aiCfg.Provider, active.Model, generated.RequestID, generated.Attempts,
		generated.Latency, generated.InputTokens, generated.OutputTokens, generated.TotalTokens, generated.FinishReason,
	)
	if err != nil {
		log.Printf("EnsureTodayAIInsight: provider=%s block=BUNDLE: %v", aiCfg.Provider, err)
		s.aiRegenLastFailAt.Store(failureKey, time.Now())
		return s.GetAIInsightCombined(today, lang)
	}
	for block, validationError := range generated.InvalidBlocks {
		log.Printf("EnsureTodayAIInsight: provider=%s block=%s validation: %s", aiCfg.Provider, block, validationError)
	}
	saveFailed := false
	for _, block := range ai.GeneratedBlockOrder {
		text := generated.Blocks[block]
		if strings.TrimSpace(text) == "" {
			continue
		}
		if err := s.SaveAIBlock(today, lang, block, text, bundleHash); err != nil {
			log.Printf("EnsureTodayAIInsight: save %s: %v", block, err)
			saveFailed = true
		}
	}
	if saveFailed || len(generated.InvalidBlocks) > 0 {
		s.aiRegenLastFailAt.Store(failureKey, time.Now())
		return s.GetAIInsightCombined(today, lang)
	}
	s.aiRegenLastFailAt.Delete(failureKey)
	return s.GetAIInsightCombined(today, lang)
}

func aiBundleCacheComplete(full map[string]*AIBlock, expectedHash string) bool {
	for _, block := range ai.GeneratedBlockOrder {
		cached := full[block]
		if cached == nil || cached.InputsHash != expectedHash || strings.TrimSpace(cached.Text) == "" {
			return false
		}
	}
	return true
}

// EnsureTodayAIInsightAsync fires EnsureTodayAIInsight in a goroutine.
// The single-flight gate (and failure backoff) lives inside
// EnsureTodayAIInsight, so concurrent callers — including sync ones from
// the morning-retry / test-notify / opportunistic-trigger paths — share
// the same dedup. Returns true when this call likely started a regen
// (best-effort signal for logging; not authoritative because the inner
// gate races with this fast-path Load).
func (s *DB) EnsureTodayAIInsightAsync(aiCfg AIConfig, lang string) bool {
	if !aiCfg.Enabled() {
		return false
	}
	key := time.Now().In(s.reportTZLocation()).Format("2006-01-02") + "|" + lang
	if _, ok := s.aiRegenInFlight.Load(key); ok {
		return false
	}
	go s.EnsureTodayAIInsight(aiCfg, lang)
	return true
}

// AIRegenInFlight reports whether a regen is currently running for (today, lang).
// Used by the /api/ai-briefing handler to set a "generating" flag in the
// response when the cache is still warming up.
func (s *DB) AIRegenInFlight(lang string) bool {
	key := time.Now().In(s.reportTZLocation()).Format("2006-01-02") + "|" + lang
	_, ok := s.aiRegenInFlight.Load(key)
	return ok
}
