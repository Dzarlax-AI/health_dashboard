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

// EnsureDailyInsightNarrativeSlotsAsync schedules only serving-eligible prose.
// Overall, sleep, recovery, and energy are independent slots: a material
// update to one can refresh its explanation without invalidating the others.
// A domain with no distinct server-approved meaning (for example, an energy
// state already expressed by the authoritative action) remains server-only.
func (s *DB) EnsureDailyInsightNarrativeSlotsAsync(snapshot *health.DailyInsightSnapshot, aiCfg AIConfig, lang string) bool {
	if snapshot == nil || !TodayInsightsB1GenerationEnabled(s, aiCfg) {
		return false
	}
	providerFingerprint := DailyInsightGenerationFingerprint(aiCfg, lang)
	scheduled := false
	for _, slot := range []string{health.DailyInsightNarrativeOverallSlot, "sleep", "recovery", "energy"} {
		input, known := health.BuildDailyInsightNarrativeSlotInput(snapshot, lang, slot)
		if !known || len(input.Slot.Claims) == 0 || !health.HasEligibleDailyInsightNarrativeSlot(snapshot, lang, slot) {
			continue
		}
		materialHash := health.DailyInsightNarrativeSlotMaterialHash(snapshot, lang, slot)
		if materialHash == "" {
			continue
		}
		// Ingestion can refresh a snapshot before the first HTTP read creates
		// its slot rows. Materialize the durable row before claiming it so the
		// first current-day background pass can actually obtain a lease.
		if err := s.UpsertDailyInsightNarrativeSlot(context.Background(), DailyInsightNarrativeSlot{
			Date: snapshot.Date, Lang: lang, Slot: slot, MaterialInputHash: materialHash, ProviderFingerprint: providerFingerprint,
		}); err != nil {
			log.Printf("daily insight narrative slot: initialize date=%s lang=%s slot=%s: %v", snapshot.Date, lang, slot, err)
			continue
		}
		key := snapshot.Date + "|" + lang + "|" + slot + "|" + materialHash + "|" + providerFingerprint
		if _, loaded := s.dailyInsightInFlight.LoadOrStore(key, struct{}{}); loaded {
			continue
		}
		scheduled = true
		go func(slot, materialHash, inFlightKey string) {
			defer s.dailyInsightInFlight.Delete(inFlightKey)
			if err := s.EnsureDailyInsightNarrativeSlot(context.Background(), snapshot, aiCfg, lang, slot, materialHash, providerFingerprint); err != nil {
				log.Printf("daily insight narrative slot: date=%s lang=%s slot=%s: %v", snapshot.Date, lang, slot, err)
			}
		}(slot, materialHash, key)
	}
	return scheduled
}

// EnsureDailyInsightNarrativeSlot generates one text overlay for a current
// snapshot. It is deliberately unable to persist or invalidate sibling slots.
func (s *DB) EnsureDailyInsightNarrativeSlot(ctx context.Context, snapshot *health.DailyInsightSnapshot, aiCfg AIConfig, lang, slot, materialHash, providerFingerprint string) error {
	input, known := health.BuildDailyInsightNarrativeSlotInput(snapshot, lang, slot)
	if !known || snapshot == nil || snapshot.Date == "" || materialHash == "" || providerFingerprint == "" {
		return fmt.Errorf("invalid daily insight narrative slot generation input")
	}
	if !TodayInsightsB1GenerationEnabled(s, aiCfg) || len(input.Slot.Claims) == 0 || !health.HasEligibleDailyInsightNarrativeSlot(snapshot, lang, slot) {
		return nil
	}
	if snapshot.Date != time.Now().In(s.reportTZLocation()).Format("2006-01-02") {
		return fmt.Errorf("refusing non-current daily insight slot generation for %s", snapshot.Date)
	}
	provider, active, err := ResolveTodayInsightsB1ProviderConfig(aiCfg)
	if err != nil {
		return err
	}
	leaseToken, err := s.ClaimDailyInsightNarrativeSlotGeneration(ctx, snapshot.Date, lang, slot, materialHash, providerFingerprint, time.Now())
	if err != nil {
		return fmt.Errorf("claim slot: %w", err)
	}
	if leaseToken == "" {
		return nil
	}
	generationCtx, cancel := context.WithTimeout(ctx, dailyInsightGenerationDeadline)
	defer cancel()
	generated, generationErr := ai.GenerateDailyInsightNarrativeSlot(generationCtx, provider, ai.ProviderConfig{
		APIKey: active.APIKey, Model: active.Model, ReasoningEffort: active.ReasoningEffort, MaxOutputTokens: active.MaxOutputTokens,
	}, snapshot, lang, slot)
	log.Printf(
		"daily insight narrative slot: provider=%s model=%s slot=%s request_id=%q attempts=%d latency=%s input_tokens=%d output_tokens=%d total_tokens=%d finish=%q",
		aiCfg.Provider, active.Model, slot, generated.RequestID, generated.Attempts, generated.Latency,
		generated.InputTokens, generated.OutputTokens, generated.TotalTokens, generated.FinishReason,
	)
	if generationErr != nil {
		_, failErr := s.FailDailyInsightNarrativeSlotGeneration(ctx, snapshot.Date, lang, slot, materialHash, providerFingerprint, leaseToken, time.Now())
		if failErr != nil {
			return fmt.Errorf("generate slot: %v; record failure: %w", generationErr, failErr)
		}
		return generationErr
	}
	payload, err := json.Marshal(health.DailyInsightNarrativeSlot{
		Version: health.DailyInsightNarrativeVersion, Locale: input.Locale,
		Slot: health.DailyInsightNarrativeDomain{Key: slot, Section: generated.Section},
	})
	if err != nil {
		return fmt.Errorf("marshal daily insight narrative slot: %w", err)
	}
	saved, err := s.SaveDailyInsightNarrativeSlot(ctx, snapshot.Date, lang, slot, materialHash, providerFingerprint, leaseToken, payload)
	if err != nil {
		_, failErr := s.FailDailyInsightNarrativeSlotGeneration(ctx, snapshot.Date, lang, slot, materialHash, providerFingerprint, leaseToken, time.Now())
		if failErr != nil {
			return fmt.Errorf("save slot: %v; record failure: %w", err, failErr)
		}
		return fmt.Errorf("save slot: %w", err)
	}
	if !saved {
		return nil
	}
	return nil
}
