package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

const dailyInsightGenerationDeadline = 2 * time.Minute

// EnsureDailyInsightNarrativeAsync schedules at most one local worker for an
// exact factual snapshot/config pair. The durable claim in
// daily_insight_bundles remains the authority across restarts and processes.
func (s *DB) EnsureDailyInsightNarrativeAsync(snapshot *health.DailyInsightSnapshot, aiCfg AIConfig, lang string) bool {
	// This is the last runtime boundary before a provider request. Callers may
	// refresh snapshots from ingest workers as well as HTTP handlers, so do not
	// rely on an upstream UI flag check: B1 needs both its boolean and the
	// reviewed provider/model/reasoning approval for this exact tenant.
	if snapshot == nil || !aiCfg.Enabled() || !TodayInsightsB1Enabled(s) || !TodayInsightsB1ApprovedForConfig(s, aiCfg) || !health.HasEligibleDailyInsightNarrativeClaims(snapshot, lang) {
		return false
	}
	materialHash := health.DailyInsightMaterialHash(snapshot)
	providerFingerprint := DailyInsightGenerationFingerprint(aiCfg, lang)
	key := snapshot.Date + "|" + lang + "|" + materialHash + "|" + providerFingerprint
	if _, loaded := s.dailyInsightInFlight.LoadOrStore(key, struct{}{}); loaded {
		return false
	}
	go func() {
		defer s.dailyInsightInFlight.Delete(key)
		if err := s.EnsureDailyInsightNarrative(context.Background(), snapshot, aiCfg, lang, materialHash, providerFingerprint); err != nil {
			log.Printf("daily insight narrative: date=%s lang=%s: %v", snapshot.Date, lang, err)
		}
	}()
	return true
}

// EnsureDailyInsightNarrative generates and persists one text overlay for an
// already-persisted current snapshot. It is today-only and never reads or
// converts legacy ai_briefing_blocks.
func (s *DB) EnsureDailyInsightNarrative(ctx context.Context, snapshot *health.DailyInsightSnapshot, aiCfg AIConfig, lang, materialHash, providerFingerprint string) error {
	if snapshot == nil || snapshot.Date == "" || materialHash == "" || providerFingerprint == "" {
		return fmt.Errorf("invalid daily insight generation input")
	}
	// Keep this guard here as well as in the async scheduler. This exported
	// method is the provider-adjacent boundary, so a future internal caller
	// cannot bypass the tenant flag or reuse an unreviewed model configuration.
	if !aiCfg.Enabled() || !TodayInsightsB1Enabled(s) || !TodayInsightsB1ApprovedForConfig(s, aiCfg) {
		return nil
	}
	if !health.HasEligibleDailyInsightNarrativeClaims(snapshot, lang) {
		return nil
	}
	if snapshot.Date != time.Now().In(s.reportTZLocation()).Format("2006-01-02") {
		return fmt.Errorf("refusing non-current daily insight generation for %s", snapshot.Date)
	}
	provider, active, err := ResolveTodayInsightsB1ProviderConfig(aiCfg)
	if err != nil {
		return err
	}
	leaseToken, err := s.ClaimDailyInsightGeneration(ctx, snapshot.Date, lang, materialHash, providerFingerprint, time.Now())
	if err != nil {
		return fmt.Errorf("claim: %w", err)
	}
	if leaseToken == "" {
		return nil
	}

	generationCtx, cancel := context.WithTimeout(ctx, dailyInsightGenerationDeadline)
	defer cancel()
	generated, generationErr := ai.GenerateDailyInsightNarrative(generationCtx, provider, ai.ProviderConfig{
		APIKey:          active.APIKey,
		Model:           active.Model,
		ReasoningEffort: active.ReasoningEffort,
		MaxOutputTokens: active.MaxOutputTokens,
	}, snapshot, lang)
	log.Printf(
		"daily insight narrative: provider=%s model=%s request_id=%q attempts=%d latency=%s input_tokens=%d output_tokens=%d total_tokens=%d finish=%q",
		aiCfg.Provider, active.Model, generated.RequestID, generated.Attempts, generated.Latency,
		generated.InputTokens, generated.OutputTokens, generated.TotalTokens, generated.FinishReason,
	)
	for domain, validationErr := range generated.InvalidDomains {
		log.Printf("daily insight narrative: domain=%s validation=%s", domain, validationErr)
	}
	if generationErr != nil {
		_, failErr := s.FailDailyInsightGeneration(ctx, snapshot.Date, lang, materialHash, providerFingerprint, leaseToken, time.Now())
		if failErr != nil {
			return fmt.Errorf("generate: %v; record failure: %w", generationErr, failErr)
		}
		return generationErr
	}
	if _, err := health.ApplyDailyInsightNarrative(snapshot, generated.Narrative); err != nil {
		_, failErr := s.FailDailyInsightGeneration(ctx, snapshot.Date, lang, materialHash, providerFingerprint, leaseToken, time.Now())
		if failErr != nil {
			return fmt.Errorf("validate narrative: %v; record failure: %w", err, failErr)
		}
		return fmt.Errorf("validate narrative: %w", err)
	}
	payload, err := json.Marshal(generated.Narrative)
	if err != nil {
		_, failErr := s.FailDailyInsightGeneration(ctx, snapshot.Date, lang, materialHash, providerFingerprint, leaseToken, time.Now())
		if failErr != nil {
			return fmt.Errorf("marshal narrative: %v; record failure: %w", err, failErr)
		}
		return fmt.Errorf("marshal narrative: %w", err)
	}
	saved, err := s.SaveDailyInsightNarrativeForGeneration(ctx, snapshot.Date, lang, materialHash, providerFingerprint, leaseToken, payload)
	if err != nil {
		_, failErr := s.FailDailyInsightGeneration(ctx, snapshot.Date, lang, materialHash, providerFingerprint, leaseToken, time.Now())
		if failErr != nil {
			return fmt.Errorf("save narrative: %v; record failure: %w", err, failErr)
		}
		return fmt.Errorf("save narrative: %w", err)
	}
	if !saved {
		// A newer snapshot, provider configuration, or reclaimed lease won.
		// This is an expected compare-and-swap outcome, never an error to retry.
		return nil
	}
	return nil
}
