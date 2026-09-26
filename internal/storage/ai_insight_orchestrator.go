package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

// AIInsightGenerationFingerprint has a new contract identity. Historical B1
// approvals and cached explanatory prose can never authorize/render this mode.
func AIInsightGenerationFingerprint(cfg AIConfig, lang string) string {
	if !cfg.Enabled() {
		return "disabled|ai-second-opinion|" + lang
	}
	_, resolved, err := ResolveTodayInsightsB1ProviderConfig(cfg)
	if err != nil {
		return "invalid|ai-second-opinion|" + lang
	}
	return ai.HashForGeneration("", ai.GenerationFingerprint{
		Provider: cfg.Provider, Model: resolved.Model, ReasoningEffort: resolved.ReasoningEffort,
		MaxOutputTokens: resolved.MaxOutputTokens,
		PromptRevision:  ai.AIInsightPromptRevision + "|" + ai.AIInsightReviewFingerprint() + "|" + lang,
	})
}

// ScheduleAIInsightSlotAsync retains the existing durable lease/CAS/backoff
// and latest-wins debounce, now keyed independently for every eligible slot.
func (s *DB) ScheduleAIInsightSlotAsync(snapshot *health.DailyInsightSnapshot, cfg AIConfig, input health.AIInsightInput) bool {
	if snapshot == nil || !TodayInsightsB1GenerationEnabled(s, cfg) || input.Version != health.AIInsightInputVersion ||
		snapshot.Date != s.dailyInsightNow().In(s.reportTZLocation()).Format("2006-01-02") {
		return false
	}
	materialHash := health.AIInsightInputHash(input)
	providerFingerprint := AIInsightGenerationFingerprint(cfg, input.Locale)
	if materialHash == "" || providerFingerprint == "" {
		return false
	}
	ctx, cancel := queryCtx()
	defer cancel()
	if err := s.UpsertDailyInsightNarrativeSlot(ctx, DailyInsightNarrativeSlot{
		Date: snapshot.Date, Lang: input.Locale, Slot: input.Slot,
		MaterialInputHash: materialHash, ProviderFingerprint: providerFingerprint,
	}); err != nil {
		log.Printf("AI insight slot: initialize date=%s lang=%s slot=%s: %v", snapshot.Date, input.Locale, input.Slot, err)
		return false
	}
	entries, err := s.GetDailyInsightNarrativeSlots(snapshot.Date, input.Locale)
	if err != nil {
		log.Printf("AI insight slot: read date=%s lang=%s slot=%s: %v", snapshot.Date, input.Locale, input.Slot, err)
		return false
	}
	if entry, found := entries[input.Slot]; found && entry.MaterialInputHash == materialHash &&
		entry.ProviderFingerprint == providerFingerprint && entry.NarrativeInputHash == materialHash &&
		entry.GenerationState == DailyInsightStateReady && len(entry.Narrative) > 0 {
		return false
	}
	key := snapshot.Date + "|" + input.Locale + "|" + input.Slot
	inputCopy := input
	work := dailyInsightNarrativeSlotWork{snapshot: snapshot, aiCfg: cfg, lang: input.Locale,
		aiInsightInput: &inputCopy, materialHash: materialHash, providerFingerprint: providerFingerprint,
		notBefore: s.dailyInsightNow().Add(s.dailyInsightDebounceWindow())}
	for {
		created := newDailyInsightNarrativeCoordinator()
		actual, loaded := s.dailyInsightInFlight.LoadOrStore(key, created)
		coordinator, ok := actual.(*dailyInsightNarrativeCoordinator)
		if !ok {
			return false
		}
		scheduled, open := coordinator.schedule(work)
		if !open {
			continue
		}
		if !loaded {
			go s.runDailyInsightNarrativeCoordinator(key, coordinator)
		}
		return scheduled || !loaded
	}
}

func (s *DB) EnsureAIInsightSlot(ctx context.Context, snapshot *health.DailyInsightSnapshot, cfg AIConfig, input health.AIInsightInput, materialHash, providerFingerprint string) error {
	if snapshot == nil || input.Version != health.AIInsightInputVersion || input.Locale == "" || input.Slot == "" ||
		materialHash == "" || providerFingerprint == "" || health.AIInsightInputHash(input) != materialHash {
		return fmt.Errorf("invalid AI insight generation input")
	}
	if !TodayInsightsB1GenerationEnabled(s, cfg) {
		return nil
	}
	if snapshot.Date != s.dailyInsightNow().In(s.reportTZLocation()).Format("2006-01-02") {
		return fmt.Errorf("refusing historical AI insight generation")
	}
	provider, active, err := ResolveTodayInsightsB1ProviderConfig(cfg)
	if err != nil {
		return err
	}
	lease, err := s.ClaimDailyInsightNarrativeSlotGeneration(ctx, snapshot.Date, input.Locale, input.Slot, materialHash, providerFingerprint, s.dailyInsightNow())
	if err != nil || lease == "" {
		return err
	}
	generationCtx, cancel := context.WithTimeout(ctx, dailyInsightGenerationDeadline)
	defer cancel()
	generated, generationErr := ai.GenerateAIInsightSlot(generationCtx, provider, ai.ProviderConfig{
		APIKey: active.APIKey, Model: active.Model, ReasoningEffort: active.ReasoningEffort, MaxOutputTokens: active.MaxOutputTokens,
	}, input)
	log.Printf("AI insight slot: provider=%s model=%s slot=%s author_request_id=%q author_attempts=%d author_latency=%s author_input_tokens=%d author_output_tokens=%d review_request_id=%q review_attempts=%d review_latency=%s review_input_tokens=%d review_output_tokens=%d finish=%q",
		cfg.Provider, active.Model, input.Slot, generated.RequestID, generated.Attempts, generated.Latency,
		generated.InputTokens, generated.OutputTokens, generated.ReviewUsage.RequestID, generated.ReviewUsage.Attempts,
		generated.ReviewUsage.Latency, generated.ReviewUsage.InputTokens, generated.ReviewUsage.OutputTokens, generated.FinishReason)
	if generationErr != nil {
		_, failErr := s.FailDailyInsightNarrativeSlotGeneration(ctx, snapshot.Date, input.Locale, input.Slot, materialHash, providerFingerprint, lease, s.dailyInsightNow())
		if failErr != nil {
			return fmt.Errorf("generate AI insight: %v; record failure: %w", generationErr, failErr)
		}
		return generationErr
	}
	var section *health.AIInsightSection
	if generated.Insight != nil {
		section = &health.AIInsightSection{Text: generated.Insight.Text, FactIDs: generated.Insight.FactIDs,
			Stance: generated.Insight.Stance, AlternativeAction: generated.Insight.AlternativeAction}
	}
	payload, err := json.Marshal(health.AIInsightSlotResponse{Version: health.AIInsightVersion,
		Locale: input.Locale, Slot: input.Slot, Insight: section})
	if err != nil {
		return err
	}
	saved, err := s.SaveDailyInsightNarrativeSlot(ctx, snapshot.Date, input.Locale, input.Slot, materialHash, providerFingerprint, lease, payload)
	if err != nil {
		_, failErr := s.FailDailyInsightNarrativeSlotGeneration(ctx, snapshot.Date, input.Locale, input.Slot, materialHash, providerFingerprint, lease, s.dailyInsightNow())
		if failErr != nil {
			return fmt.Errorf("save AI insight: %v; record failure: %w", err, failErr)
		}
		return err
	}
	if !saved {
		return nil // stale material or a lost lease cannot be rendered
	}
	return nil
}
