package storage

import (
	"context"
	"encoding/json"
	"health-receiver/internal/health"
)

// CurrentCachedAIInsight follows the same exact-material validation as the
// Today API, without scheduling, leasing, invalidation or provider calls.
func (s *DB) CurrentCachedAIInsight(ctx context.Context, snapshot *health.DailyInsightSnapshot, cfg AIConfig, lang, slot string) (*health.DailyInsightAIInsight, error) {
	if snapshot == nil || !TodayAIInsightGenerationEnabled(s, cfg) {
		return nil, nil
	}
	input, eligible := health.BuildAIInsightInput(snapshot, lang, slot, nil)
	if !eligible {
		return nil, nil
	}
	entries, err := s.GetDailyInsightNarrativeSlots(snapshot.Date, lang)
	if err != nil {
		return nil, err
	}
	entry, found := entries[slot]
	hash := health.AIInsightInputHash(input)
	if !found || entry.GenerationState != DailyInsightStateReady || entry.MaterialInputHash != hash || entry.NarrativeInputHash != hash || entry.ProviderFingerprint != AIInsightGenerationFingerprint(cfg, lang) || len(entry.Narrative) == 0 {
		return nil, nil
	}
	var candidate health.AIInsightSlotResponse
	if err = json.Unmarshal(entry.Narrative, &candidate); err != nil {
		return nil, err
	}
	return health.ValidateAIInsightSlot(input, candidate)
}
