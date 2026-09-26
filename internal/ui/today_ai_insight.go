package ui

import (
	"context"
	"encoding/json"
	"log"
	"time"

	clientapi "health-receiver/internal/api"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

// resolveTodayAIInsights keeps the provider packet separate from the rendered
// snapshot. Domain opinions settle first; overall sees the accepted siblings
// or explicit fallback markers and is re-keyed when a late sibling changes.
func resolveTodayAIInsights(ctx context.Context, db *storage.DB, snapshot *health.DailyInsightSnapshot,
	cfg storage.AIConfig, lang, providerFingerprint string) (*health.DailyInsightSnapshot, []clientapi.TodayInsightSlotGeneration, error) {
	entries, err := db.GetDailyInsightNarrativeSlots(snapshot.Date, lang)
	if err != nil {
		return nil, nil, err
	}
	rendered := snapshot
	states := make([]clientapi.TodayInsightSlotGeneration, 0, 4)
	siblings := make([]health.AIInsightSibling, 0, 3)
	allDomainsTerminal := true
	for _, slot := range []string{"sleep", "recovery", "energy"} {
		input, eligible := health.BuildAIInsightInput(snapshot, lang, slot, nil)
		if !eligible {
			states = append(states, clientapi.TodayInsightSlotGeneration{Key: slot, State: storage.DailyInsightStateDisabled, FreshForSnapshot: true})
			siblings = append(siblings, health.AIInsightSibling{Slot: slot, State: storage.DailyInsightStateDisabled})
			continue
		}
		var insight *health.DailyInsightAIInsight
		var state clientapi.TodayInsightSlotGeneration
		rendered, insight, state, err = resolveOneTodayAIInsight(ctx, db, rendered, snapshot, cfg, input, providerFingerprint, entries)
		if err != nil {
			return nil, nil, err
		}
		states = append(states, state)
		sibling := health.AIInsightSibling{Slot: slot, State: state.State}
		if insight != nil {
			sibling.Text = insight.Text
			sibling.AlternativeAction = insight.AlternativeAction
		}
		siblings = append(siblings, sibling)
		if state.State == storage.DailyInsightStateCold || state.State == storage.DailyInsightStateGenerating {
			allDomainsTerminal = false
		}
	}
	input, eligible := health.BuildAIInsightInput(snapshot, lang, health.DailyInsightNarrativeOverallSlot, siblings)
	if !eligible {
		states = append(states, clientapi.TodayInsightSlotGeneration{Key: health.DailyInsightNarrativeOverallSlot, State: storage.DailyInsightStateDisabled, FreshForSnapshot: true})
		return rendered, states, nil
	}
	if !allDomainsTerminal {
		states = append(states, clientapi.TodayInsightSlotGeneration{Key: health.DailyInsightNarrativeOverallSlot, State: storage.DailyInsightStateCold, FreshForSnapshot: false})
		return rendered, states, nil
	}
	var state clientapi.TodayInsightSlotGeneration
	rendered, _, state, err = resolveOneTodayAIInsight(ctx, db, rendered, snapshot, cfg, input, providerFingerprint, entries)
	if err != nil {
		return nil, nil, err
	}
	states = append(states, state)
	return rendered, states, nil
}

func resolveOneTodayAIInsight(ctx context.Context, db *storage.DB, rendered, generationSnapshot *health.DailyInsightSnapshot,
	cfg storage.AIConfig, input health.AIInsightInput, providerFingerprint string,
	entries map[string]storage.DailyInsightNarrativeSlot) (*health.DailyInsightSnapshot, *health.DailyInsightAIInsight, clientapi.TodayInsightSlotGeneration, error) {
	hash := health.AIInsightInputHash(input)
	state := clientapi.TodayInsightSlotGeneration{Key: input.Slot, State: storage.DailyInsightStateCold}
	if err := db.UpsertDailyInsightNarrativeSlot(ctx, storage.DailyInsightNarrativeSlot{
		Date: generationSnapshot.Date, Lang: input.Locale, Slot: input.Slot,
		MaterialInputHash: hash, ProviderFingerprint: providerFingerprint,
	}); err != nil {
		return nil, nil, state, err
	}
	entry, found := entries[input.Slot]
	state.FreshForSnapshot = todayInsightNarrativeSlotEntryFresh(entry, found, hash, providerFingerprint)
	if state.FreshForSnapshot && entry.GenerationState != "" {
		state.State = entry.GenerationState
	}
	if state.State == storage.DailyInsightStateReady && (entry.NarrativeInputHash != hash || len(entry.Narrative) == 0) {
		state.State = storage.DailyInsightStateFailed
	}
	var insight *health.DailyInsightAIInsight
	if state.State == storage.DailyInsightStateReady {
		var candidate health.AIInsightSlotResponse
		validationErr := json.Unmarshal(entry.Narrative, &candidate)
		if validationErr == nil {
			insight, validationErr = health.ValidateAIInsightSlot(input, candidate)
		}
		if validationErr == nil && insight == nil {
			// A deliberate provider null is terminal for this exact material.
			// Keep the Server Insight; a later material hash may try again.
			state.State = storage.DailyInsightStateDisabled
		} else if validationErr != nil {
			state.State = storage.DailyInsightStateFailed
			if invalidateErr := db.InvalidateDailyInsightNarrativeSlot(ctx, generationSnapshot.Date, input.Locale, input.Slot, hash, providerFingerprint); invalidateErr != nil {
				log.Printf("today AI insight: invalidate slot %s: %v", input.Slot, invalidateErr)
			}
		} else if applied, applyErr := health.ApplyAIInsightSlot(rendered, input.Slot, insight); applyErr != nil {
			state.State = storage.DailyInsightStateFailed
			log.Printf("today AI insight: apply slot %s: %v", input.Slot, applyErr)
		} else {
			rendered = applied
		}
	}
	if entry.RetryAfter != nil && entry.RetryAfter.After(time.Now()) {
		state.RetryAfterSeconds = int(time.Until(*entry.RetryAfter).Seconds())
	}
	if state.State != storage.DailyInsightStateReady && state.State != storage.DailyInsightStateDisabled {
		db.ScheduleAIInsightSlotAsync(generationSnapshot, cfg, input)
	}
	return rendered, insight, state, nil
}
