package ui

import (
	"testing"

	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

func TestTodayInsightsNarrativeStateRendersOnlyFreshOverallEntry(t *testing.T) {
	entry := storage.DailyInsightNarrativeSlot{
		Slot: health.DailyInsightNarrativeOverallSlot, MaterialInputHash: "current", ProviderFingerprint: "review-current",
		NarrativeInputHash: "current", GenerationState: storage.DailyInsightStateReady, Narrative: []byte(`{"slot":"overall"}`),
	}
	if !todayInsightNarrativeSlotEntryFresh(entry, true, "current", "review-current") {
		t.Fatal("exact current overall entry was not accepted")
	}
	if todayInsightNarrativeSlotEntryFresh(entry, true, "old", "review-current") {
		t.Fatal("stale material entry would render")
	}
	if todayInsightNarrativeSlotEntryFresh(entry, true, "current", "review-old") {
		t.Fatal("entry from an old provider review would render")
	}
	for _, slot := range todayInsightNarrativeSlots {
		if slot == health.DailyInsightNarrativeOverallSlot {
			continue
		}
		if slot != "sleep" && slot != "recovery" && slot != "energy" {
			t.Fatalf("unexpected compatibility slot %q", slot)
		}
	}
}

func TestTodayInsightsIneligibleOverallHasNoColdLifecycleState(t *testing.T) {
	slots := disabledTodayInsightNarrativeSlots(true)
	if len(slots) != len(todayInsightNarrativeSlots) {
		t.Fatalf("disabled slots = %d, want %d", len(slots), len(todayInsightNarrativeSlots))
	}
	for _, slot := range slots {
		if slot.State != storage.DailyInsightStateDisabled || !slot.FreshForSnapshot {
			t.Fatalf("ineligible slot state = %#v, want disabled and fresh", slot)
		}
	}
	state, retryAfter := aggregateTodayInsightGeneration(slots)
	if state != storage.DailyInsightStateDisabled || retryAfter != 0 {
		t.Fatalf("ineligible aggregate = (%q, %d), want disabled/0", state, retryAfter)
	}
}
