package storage

import (
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

// EnsureTodayAIInsight regenerates the four AI blocks (SLEEP, YESTERDAY,
// RECOVERY, RECOMMENDATION) selectively: each leaf is keyed by an
// inputs_hash over the metrics it depends on, so a late HRV update only
// invalidates the blocks that actually read HRV. Returns the joined
// insight (legacy callers still expect a single string), or "" if AI is
// disabled / no metrics exist.
//
// Safe to call repeatedly — cached rows whose inputs_hash matches the
// current data are skipped, and only the leaves whose hashes diverged hit
// the active provider. RECOMMENDATION re-runs whenever any leaf text changes
// or when EnergyBank.action_verdict has rotated.
//
// Concurrency: only one EnsureTodayAIInsight per (date, lang) runs at a
// time across the process. Concurrent calls (sync morning-retry vs async
// poller-driven regen) return the current cache instead of duplicating
// provider work. After a failure, retries are throttled to once per
// aiRegenFailBackoff so an upstream outage doesn't compound.
func (s *DB) EnsureTodayAIInsight(aiCfg AIConfig, lang string) string {
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
	if maxOutputTokens <= 0 {
		maxOutputTokens = ai.DefaultMaxOutputTokens
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

	// Single-flight gate. If another caller is already regenerating, return
	// the (possibly empty) cache rather than fanning out. Caller will see
	// the cache populate on the next /api/ai-briefing poll.
	if _, loaded := s.aiRegenInFlight.LoadOrStore(key, true); loaded {
		return s.GetAIInsightCombined(today, lang)
	}
	defer s.aiRegenInFlight.Delete(key)

	// Failure backoff: skip regen for `aiRegenFailBackoff` after the last
	// run produced zero usable blocks (upstream outage / quota / auth fail).
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

	// EnergyBank lives on the briefing response — fetch it for the recovery
	// hash and to give RECOMMENDATION the action_verdict context it must
	// align with.
	briefing, err := s.GetHealthBriefing(lang)
	if err != nil {
		log.Printf("EnsureTodayAIInsight: briefing: %v", err)
		return ""
	}
	var eb *health.EnergyBank
	var insightCtx ai.InsightContext
	if briefing != nil {
		eb = briefing.EnergyBank
		insightCtx = aiContextFromBriefing(briefing)
	}

	metricsJSON, err := json.Marshal(raw)
	if err != nil {
		log.Printf("EnsureTodayAIInsight: marshal: %v", err)
		return ""
	}
	recoveryJSON, err := json.Marshal(struct {
		Metrics *health.RawMetrics `json:"metrics"`
		Context ai.InsightContext  `json:"context"`
	}{Metrics: raw, Context: insightCtx})
	if err != nil {
		log.Printf("EnsureTodayAIInsight: marshal recovery context: %v", err)
		return ""
	}

	hashes := map[string]string{
		ai.BlockSleep:     ai.HashForGeneration(ai.HashSleep(raw), fingerprint),
		ai.BlockYesterday: ai.HashForGeneration(ai.HashYesterday(raw), fingerprint),
		ai.BlockRecovery:  ai.HashForGeneration(ai.HashRecovery(raw, eb, insightCtx), fingerprint),
	}

	cached := s.GetAIBlocksFull(today, lang)

	skip := func(block string) bool {
		row := cached[block]
		return row != nil && row.InputsHash == hashes[block] && strings.TrimSpace(row.Text) != ""
	}

	saved := 0
	payloadForBlock := func(block string) []byte {
		if block == ai.BlockRecovery {
			return recoveryJSON
		}
		return metricsJSON
	}
	results := ai.GenerateLeafBlocks(provider, providerCfg, payloadForBlock, lang, skip)
	for _, r := range results {
		if r.Err != nil {
			log.Printf("EnsureTodayAIInsight: provider=%s block=%s: %v", aiCfg.Provider, r.Block, r.Err)
			continue
		}
		if strings.TrimSpace(r.Text) == "" {
			log.Printf("EnsureTodayAIInsight: provider=%s block=%s returned empty content, not caching", aiCfg.Provider, r.Block)
			continue
		}
		if err := s.SaveAIBlock(today, lang, r.Block, r.Text, hashes[r.Block]); err != nil {
			log.Printf("EnsureTodayAIInsight: save %s: %v", r.Block, err)
			continue
		}
		cached[r.Block] = &AIBlock{Block: r.Block, Text: r.Text, InputsHash: hashes[r.Block]}
		saved++
	}

	textOf := func(block string) string {
		if b := cached[block]; b != nil {
			return b.Text
		}
		return ""
	}
	sleepText := textOf(ai.BlockSleep)
	yesterdayText := textOf(ai.BlockYesterday)
	recoveryText := textOf(ai.BlockRecovery)
	// Pull the last 7 EOD verdict snapshots so RECOMMENDATION can pick up
	// multi-day patterns ("3 rest days in a row -> push for proper rest")
	// instead of treating each day in isolation. Frozen past values are
	// safe to hash — see HashRecommendation doc on why intra-day EnergyBank
	// fields are excluded.
	verdictHistory := []string{}
	if hist, herr := s.GetEnergyHistory(7); herr == nil {
		for _, p := range hist {
			if p.Verdict != "" {
				verdictHistory = append(verdictHistory, p.Verdict)
			}
		}
	}
	recHash := ai.HashForGeneration(
		ai.HashRecommendation(sleepText, yesterdayText, recoveryText, eb, verdictHistory, insightCtx),
		fingerprint,
	)
	recRow := cached[ai.BlockRecommendation]
	if recRow == nil || recRow.InputsHash != recHash || strings.TrimSpace(recRow.Text) == "" {
		var stressFlags []string
		if eb != nil {
			stressFlags = eb.Flags
		}
		recText, err := ai.GenerateRecommendation(provider, providerCfg, recoveryJSON, lang,
			sleepText, yesterdayText, recoveryText, verdictHistory, stressFlags, insightCtx)
		if err != nil {
			log.Printf("EnsureTodayAIInsight: provider=%s block=RECOMMENDATION: %v", aiCfg.Provider, err)
		} else if strings.TrimSpace(recText) == "" {
			log.Printf("EnsureTodayAIInsight: provider=%s block=RECOMMENDATION returned empty content, not caching", aiCfg.Provider)
		} else if err := s.SaveAIBlock(today, lang, ai.BlockRecommendation, recText, recHash); err != nil {
			log.Printf("EnsureTodayAIInsight: save RECOMMENDATION: %v", err)
		} else {
			saved++
		}
	}

	// Track sustained failures so the next aiRegenFailBackoff window short-
	// circuits provider calls. On success, clear the timestamp.
	if saved == 0 {
		s.aiRegenLastFailAt.Store(failureKey, time.Now())
	} else {
		s.aiRegenLastFailAt.Delete(failureKey)
	}

	return s.GetAIInsightCombined(today, lang)
}

func aiContextFromBriefing(b *health.BriefingResponse) ai.InsightContext {
	if b == nil {
		return ai.InsightContext{AIAdviceMode: "withheld"}
	}
	mode := "confident_advice_allowed"
	switch b.ReadinessConfidence {
	case health.ReadinessConfidenceLow:
		mode = "provisional_explanation_only"
	case health.ReadinessConfidenceProvisional:
		mode = "provisional_explanation_only"
	}
	if b.ReadinessCapReason == "missing_same_day_evidence" {
		mode = "needs_regeneration_after_sync"
	}
	ctx := ai.InsightContext{
		ReadinessScore:      b.ReadinessScore,
		ReadinessRawScore:   b.ReadinessRawScore,
		ReadinessConfidence: b.ReadinessConfidence,
		ReadinessCapReason:  b.ReadinessCapReason,
		AIAdviceMode:        mode,
	}
	if b.SubjectiveCheckin != nil {
		ctx.CheckinStatus = b.SubjectiveCheckin.Status
		ctx.CheckinAnswer = b.SubjectiveCheckin.Answer
	}
	return ctx
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
