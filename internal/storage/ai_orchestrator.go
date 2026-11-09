package storage

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"health-receiver/internal/ai"
)

// aiRegenFailBackoff is how long we wait after a failed regen before
// retrying. Keeps a sustained upstream outage from amplifying into one
// regen attempt per polling tick.
const aiRegenFailBackoff = 5 * time.Minute

// EnsureTodayAIInsight generates one structured six-block briefing from a
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
// provider call for one date-aligned evidence packet and stores six aligned
// rows under their existing compatibility keys.
func (s *DB) EnsureTodayAIInsightContext(ctx context.Context, aiCfg AIConfig, lang string) string {
	today := time.Now().In(s.reportTZLocation()).Format("2006-01-02")
	status := AIGenerationStatus{Pipeline: "morning", Date: today, Lang: lang, Slot: "bundle", Trigger: "ensure", State: DailyInsightStateDisabled, ReasonCode: "disabled"}
	record := func() { _ = s.saveAIGenerationStatus(ctx, status) }
	if !aiCfg.Enabled() {
		record()
		return ""
	}
	provider, providerCfg, fingerprint, err := morningGenerationConfig(aiCfg)
	if err != nil {
		log.Printf("EnsureTodayAIInsight: %v", err)
		return ""
	}
	key := today + "|" + lang

	if _, loaded := s.aiRegenInFlight.LoadOrStore(key, true); loaded {
		return s.GetAIInsightCombined(today, lang)
	}
	defer s.aiRegenInFlight.Delete(key)

	raw := s.GetRawMetrics()
	if raw == nil {
		status.ReasonCode = "no_eligible_facts"
		record()
		log.Println("EnsureTodayAIInsight: no raw metrics available")
		return ""
	}
	briefing, err := s.GetHealthBriefing(lang)
	if err != nil {
		log.Printf("EnsureTodayAIInsight: briefing: %v", err)
		return ""
	}
	evidence, err := s.morningInsightEvidence(ctx, briefing, raw, today, lang)
	if err != nil {
		log.Printf("EnsureTodayAIInsight: night evidence: %v", err)
		return ""
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		log.Printf("EnsureTodayAIInsight: marshal evidence: %v", err)
		return ""
	}
	bundleHash := morningBundleHash(evidence, briefing, fingerprint)
	status.MaterialHash, status.ProviderFingerprint = bundleHash, ai.HashForGeneration("", fingerprint)
	failureKey := morningAIFailureKey(today, lang, bundleHash)
	if s.morningAIRecentlyFailed(failureKey) {
		status.State, status.ReasonCode = DailyInsightStateFailed, "backoff"
		record()
		return s.GetAIInsightCombined(today, lang)
	}
	if aiBundleCacheComplete(s.GetAIBlocksFull(today, lang), bundleHash) {
		status.State, status.ReasonCode = DailyInsightStateReady, "ready"
		record()
		s.aiRegenLastFailAt.Delete(failureKey)
		return s.GetAIInsightCombined(today, lang)
	}

	attemptedAt := time.Now()
	status.State, status.ReasonCode, status.LastAttemptAt = DailyInsightStateGenerating, "provider_started", &attemptedAt
	record()
	generated, err := ai.GenerateInsightBundle(ctx, provider, providerCfg, evidenceJSON, lang)
	log.Printf(
		"EnsureTodayAIInsight: provider=%s model=%s block=BUNDLE request_id=%q attempts=%d latency=%s input_tokens=%d output_tokens=%d total_tokens=%d finish=%q",
		aiCfg.Provider, providerCfg.Model, generated.RequestID, generated.Attempts,
		generated.Latency, generated.InputTokens, generated.OutputTokens, generated.TotalTokens, generated.FinishReason,
	)
	for block, validationError := range generated.InvalidBlocks {
		log.Printf("EnsureTodayAIInsight: provider=%s block=%s validation: %s", aiCfg.Provider, block, validationError)
	}
	if err != nil {
		retry := time.Now().Add(aiRegenFailBackoff)
		status.State, status.ReasonCode, status.RetryAfter = DailyInsightStateFailed, "provider_error", &retry
		if len(generated.InvalidBlocks) > 0 {
			status.ReasonCode = "review_rejected"
		}
		record()
		log.Printf("EnsureTodayAIInsight: provider=%s block=BUNDLE: %v", aiCfg.Provider, err)
		s.recordMorningAIFailure(failureKey)
		return s.GetAIInsightCombined(today, lang)
	}
	if err := s.SaveAIBundle(today, lang, generated.Blocks, bundleHash); err != nil {
		status.State, status.ReasonCode = DailyInsightStateFailed, "cache_write_failed"
		record()
		log.Printf("EnsureTodayAIInsight: save bundle: %v", err)
		s.recordMorningAIFailure(failureKey)
		return s.GetAIInsightCombined(today, lang)
	}
	s.aiRegenLastFailAt.Delete(failureKey)
	succeededAt := time.Now()
	status.State, status.ReasonCode, status.LastSuccessAt = DailyInsightStateReady, "ready", &succeededAt
	record()
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
	return s.EnsureTodayAIInsightAsyncContext(context.Background(), aiCfg, lang)
}

// EnsureTodayAIInsightAsyncContext bounds scheduler generation and follows shutdown.
func (s *DB) EnsureTodayAIInsightAsyncContext(ctx context.Context, aiCfg AIConfig, lang string) bool {
	if !aiCfg.Enabled() {
		return false
	}
	key := time.Now().In(s.reportTZLocation()).Format("2006-01-02") + "|" + lang
	if _, ok := s.aiRegenInFlight.Load(key); ok {
		return false
	}
	go func() {
		generationCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		s.EnsureTodayAIInsightContext(generationCtx, aiCfg, lang)
	}()
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

// morningAIFailureKey binds provider backoff to the exact current evidence and
// generation fingerprint, both already included in bundleHash.
func morningAIFailureKey(date, lang, bundleHash string) string {
	return date + "|" + lang + "|" + bundleHash
}

func (s *DB) morningAIRecentlyFailed(key string) bool {
	value, ok := s.aiRegenLastFailAt.Load(key)
	if !ok {
		return false
	}
	failedAt, ok := value.(time.Time)
	return ok && time.Since(failedAt) < aiRegenFailBackoff
}

// Expired failures must not accumulate for every changed input packet.
func (s *DB) recordMorningAIFailure(key string) {
	now := time.Now()
	s.aiRegenLastFailAt.Range(func(k, value any) bool {
		failedAt, ok := value.(time.Time)
		if !ok || now.Sub(failedAt) >= aiRegenFailBackoff {
			s.aiRegenLastFailAt.Delete(k)
		}
		return true
	})
	s.aiRegenLastFailAt.Store(key, now)
}
