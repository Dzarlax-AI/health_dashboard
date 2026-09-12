package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestDailyInsightNarrativeSlotKeepsPreviousTextUnreadableAfterMaterialChange(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := db.EnsureDailyInsightNarrativeSlotsTableContext(ctx); err != nil {
		t.Fatalf("ensure narrative slots table: %v", err)
	}
	const date, lang, slot, fingerprint = "2026-09-13", "en", "sleep", "provider-fingerprint"
	if err := db.UpsertDailyInsightNarrativeSlot(ctx, DailyInsightNarrativeSlot{
		Date: date, Lang: lang, Slot: slot, MaterialInputHash: "old-material", ProviderFingerprint: fingerprint,
	}); err != nil {
		t.Fatalf("upsert old slot: %v", err)
	}
	lease, err := db.ClaimDailyInsightNarrativeSlotGeneration(ctx, date, lang, slot, "old-material", fingerprint, time.Now())
	if err != nil || lease == "" {
		t.Fatalf("claim old slot: lease=%q err=%v", lease, err)
	}
	payload, err := json.Marshal(map[string]any{"version": "test", "slot": slot})
	if err != nil {
		t.Fatal(err)
	}
	if saved, err := db.SaveDailyInsightNarrativeSlot(ctx, date, lang, slot, "old-material", fingerprint, lease, payload); err != nil || !saved {
		t.Fatalf("save old slot: saved=%v err=%v", saved, err)
	}
	if err := db.UpsertDailyInsightNarrativeSlot(ctx, DailyInsightNarrativeSlot{
		Date: date, Lang: lang, Slot: slot, MaterialInputHash: "new-material", ProviderFingerprint: fingerprint,
	}); err != nil {
		t.Fatalf("upsert new material: %v", err)
	}
	entries, err := db.GetDailyInsightNarrativeSlots(date, lang)
	if err != nil {
		t.Fatalf("read slots: %v", err)
	}
	entry, found := entries[slot]
	if !found || entry.GenerationState != DailyInsightStateCold || entry.MaterialInputHash != "new-material" {
		t.Fatalf("changed slot = %#v", entry)
	}
	if entry.NarrativeInputHash != "old-material" || len(entry.Narrative) == 0 {
		t.Fatalf("previous narrative was not retained as a stale audit record: %#v", entry)
	}
}
