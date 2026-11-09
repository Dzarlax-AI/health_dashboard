package storage

import (
	"context"
	"encoding/json"
	"time"

	"health-receiver/internal/health"
)

// RefreshCurrentAIInsights is the shared post-derived/restart/tick entry point.
// Provider work is asynchronous and uses the existing exact-material leases.
func (s *DB) RefreshCurrentAIInsights(ctx context.Context, cfg AIConfig, lang, trigger string) error {
	if !TodayAIInsightGenerationEnabled(s, cfg) {
		return nil
	}
	snapshot, err := s.BuildTodayInsightSnapshot(ctx, lang, s.dailyInsightNow())
	if err != nil {
		return err
	}
	return s.ScheduleSnapshotAIInsights(ctx, snapshot, cfg, lang, trigger)
}

func (s *DB) ScheduleSnapshotAIInsights(ctx context.Context, snapshot *health.DailyInsightSnapshot, cfg AIConfig, lang, trigger string) error {
	if snapshot == nil || snapshot.Date != s.dailyInsightNow().In(s.reportTZLocation()).Format("2006-01-02") {
		return nil
	}
	entries, err := s.GetDailyInsightNarrativeSlots(snapshot.Date, lang)
	if err != nil {
		return err
	}
	siblings := []health.AIInsightSibling{}
	terminal := true
	for _, slot := range []string{"sleep", "recovery", "energy"} {
		input, eligible := health.BuildAIInsightInput(snapshot, lang, slot, nil)
		if input.Locale == "" {
			input.Locale = lang
		}
		sibling, err := s.refreshAIInsightSlot(ctx, snapshot, cfg, input, slot, eligible, trigger, entries)
		if err != nil {
			return err
		}
		siblings = append(siblings, sibling)
		if sibling.State == DailyInsightStateCold || sibling.State == DailyInsightStateGenerating {
			terminal = false
		}
	}
	input, eligible := health.BuildAIInsightInput(snapshot, lang, "overall", siblings)
	if input.Locale == "" {
		input.Locale = lang
	}
	if !terminal {
		s.observeAIGenerationStatus(ctx, AIGenerationStatus{Pipeline: "today", Date: snapshot.Date, Lang: lang, Slot: "overall", State: DailyInsightStateCold, ReasonCode: "waiting_for_domains", Trigger: trigger})
		return nil
	}
	_, err = s.refreshAIInsightSlot(ctx, snapshot, cfg, input, "overall", eligible, trigger, entries)
	return err
}

func (s *DB) refreshAIInsightSlot(ctx context.Context, snapshot *health.DailyInsightSnapshot, cfg AIConfig, input health.AIInsightInput, slot string, eligible bool, trigger string, entries map[string]DailyInsightNarrativeSlot) (health.AIInsightSibling, error) {
	lang := input.Locale
	if lang == "" {
		lang = "en"
	}
	sibling := health.AIInsightSibling{Slot: slot, State: DailyInsightStateDisabled}
	status := AIGenerationStatus{Pipeline: "today", Date: snapshot.Date, Lang: lang, Slot: slot, State: DailyInsightStateDisabled, ReasonCode: "no_eligible_facts", Trigger: trigger}
	if !TodayAIInsightGenerationEnabled(s, cfg) {
		status.ReasonCode = "disabled"
		s.observeAIGenerationStatus(ctx, status)
		return sibling, nil
	}
	if !eligible {
		s.observeAIGenerationStatus(ctx, status)
		return sibling, nil
	}
	hash := health.AIInsightInputHash(input)
	fingerprint := AIInsightGenerationFingerprint(cfg, lang)
	status.MaterialHash, status.ProviderFingerprint = hash, fingerprint
	entry, found := entries[slot]
	if found && entry.MaterialInputHash == hash && entry.ProviderFingerprint == fingerprint {
		sibling.State = entry.GenerationState
		status.State = entry.GenerationState
		status.RetryAfter = entry.RetryAfter
		if entry.GenerationState == DailyInsightStateReady && entry.NarrativeInputHash == hash {
			var candidate health.AIInsightSlotResponse
			if err := json.Unmarshal(entry.Narrative, &candidate); err == nil {
				if insight, err := health.ValidateAIInsightSlot(input, candidate); err == nil {
					if insight == nil {
						sibling.State = DailyInsightStateDisabled
						status.State = DailyInsightStateDisabled
						status.ReasonCode = "no_added_value"
					} else {
						sibling.Text = insight.Text
						sibling.AlternativeAction = insight.AlternativeAction
						status.ReasonCode = "ready"
					}
					s.observeAIGenerationStatus(ctx, status)
					return sibling, nil
				}
			}
			if err := s.InvalidateDailyInsightNarrativeSlot(ctx, snapshot.Date, lang, slot, hash, fingerprint); err != nil {
				return sibling, err
			}
		}
		if entry.RetryAfter != nil && entry.RetryAfter.After(s.dailyInsightNow()) {
			status.ReasonCode = "backoff"
			s.observeAIGenerationStatus(ctx, status)
			return sibling, nil
		}
		if entry.LeaseUntil != nil && entry.LeaseUntil.After(s.dailyInsightNow()) {
			status.ReasonCode = "lease_busy"
			s.observeAIGenerationStatus(ctx, status)
			return sibling, nil
		}
	}
	sibling.State = DailyInsightStateCold
	status.State = DailyInsightStateCold
	status.ReasonCode = "debounce"
	s.ScheduleAIInsightSlotAsync(snapshot, cfg, input)
	s.observeAIGenerationStatus(ctx, status)
	return sibling, nil
}

func (s *DB) recordAIInsightAttempt(ctx context.Context, snapshot *health.DailyInsightSnapshot, input health.AIInsightInput, fingerprint, reason string, success bool) {
	now := s.dailyInsightNow()
	var current bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM daily_insight_narrative_slots WHERE date=$1 AND lang=$2 AND slot=$3 AND material_input_hash=$4 AND provider_fingerprint=$5)`, snapshot.Date, input.Locale, input.Slot, health.AIInsightInputHash(input), fingerprint).Scan(&current); err != nil || !current {
		return
	}
	status := AIGenerationStatus{Pipeline: "today", Date: snapshot.Date, Lang: input.Locale, Slot: input.Slot, MaterialHash: health.AIInsightInputHash(input), ProviderFingerprint: fingerprint, State: DailyInsightStateGenerating, ReasonCode: reason, LastAttemptAt: &now}
	if success {
		status.State = DailyInsightStateReady
		status.LastSuccessAt = &now
	} else if reason != "provider_started" {
		status.State = DailyInsightStateFailed
		retry := now.Add(5 * time.Minute)
		status.RetryAfter = &retry
	}
	_ = s.saveAIGenerationStatus(ctx, status)
}
