package storage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestLastGoodAIInsightSurvivesReplacementLifecycle(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := db.EnsureDailyInsightNarrativeSlotsTableContext(ctx); err != nil {
		t.Fatal(err)
	}
	save := func(date, hash, text string) {
		t.Helper()
		if err := db.UpsertDailyInsightNarrativeSlot(ctx, DailyInsightNarrativeSlot{Date: date, Lang: "en", Slot: "sleep", MaterialInputHash: hash, ProviderFingerprint: "provider"}); err != nil {
			t.Fatal(err)
		}
		lease, err := db.ClaimDailyInsightNarrativeSlotGeneration(ctx, date, "en", "sleep", hash, "provider", time.Now())
		if err != nil || lease == "" {
			t.Fatalf("lease=%q err=%v", lease, err)
		}
		var section *health.AIInsightSection
		if text != "" {
			section = &health.AIInsightSection{Text: text, Stance: "qualify", FactIDs: []string{"sleep_current"}}
		}
		payload, _ := json.Marshal(health.AIInsightSlotResponse{Version: health.AIInsightVersion, Locale: "en", Slot: "sleep", Insight: section})
		accepted, err := db.SaveDailyInsightNarrativeSlot(ctx, date, "en", "sleep", hash, "provider", lease, payload)
		if err != nil || !accepted {
			t.Fatalf("save=%v err=%v", accepted, err)
		}
	}
	read := func(date string) LastGoodAIInsight {
		t.Helper()
		v, err := db.GetLastGoodAIInsights(ctx, "en", date)
		if err != nil {
			t.Fatal(err)
		}
		return v["sleep"]
	}
	const date = "2026-10-04"
	save(date, "first", "Previous accepted insight")
	first := read(date)
	// A new service instance uses durable DB history without process-local state.
	restarted := &DB{pool: db.pool}
	if v, err := restarted.GetLastGoodAIInsights(ctx, "en", date); err != nil || v["sleep"].Insight == nil {
		t.Fatalf("restart lost accepted text: %#v %v", v, err)
	}
	if first.Insight == nil || first.GeneratedAt.IsZero() {
		t.Fatalf("missing retained result: %#v", first)
	}
	if err := db.UpsertDailyInsightNarrativeSlot(ctx, DailyInsightNarrativeSlot{Date: date, Lang: "en", Slot: "sleep", MaterialInputHash: "changed", ProviderFingerprint: "provider"}); err != nil {
		t.Fatal(err)
	}
	lease, err := db.ClaimDailyInsightNarrativeSlotGeneration(ctx, date, "en", "sleep", "changed", "provider", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.FailDailyInsightNarrativeSlotGeneration(ctx, date, "en", "sleep", "changed", "provider", lease, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := read(date); got.Insight.Text != first.Insight.Text || !got.GeneratedAt.Equal(first.GeneratedAt) {
		t.Fatalf("failure changed previous answer: %#v", got)
	}
	save(date, "null-result", "")
	if got := read("2026-10-05"); got.Insight.Text != first.Insight.Text || got.Date != date {
		t.Fatalf("null or day boundary erased history: %#v", got)
	}
	save("2026-10-05", "new-day", "New accepted insight")
	save(date, "late-old-day", "Late old-day insight")
	if got := read("2026-10-05"); got.Insight.Text != "New accepted insight" || got.Date != "2026-10-05" {
		t.Fatalf("older writer replaced newer day: %#v", got)
	}
	payload, _ := json.Marshal(health.AIInsightSlotResponse{Version: health.AIInsightVersion, Locale: "en", Slot: "sleep", Insight: &health.AIInsightSection{Text: "Lost lease", Stance: "qualify"}})
	if saved, err := db.SaveDailyInsightNarrativeSlot(ctx, "2026-10-05", "en", "sleep", "new-day", "provider", "lost-lease", payload); err != nil || saved {
		t.Fatalf("lost lease accepted=%v err=%v", saved, err)
	}
	if got := read("2026-10-05"); got.Insight.Text != "New accepted insight" {
		t.Fatal("lost lease changed retained answer")
	}
	if other, err := db.GetLastGoodAIInsights(ctx, "ru", "2026-10-05"); err != nil || len(other) != 0 {
		t.Fatalf("cross-language leak: %#v %v", other, err)
	}
	if err := db.InvalidateDailyInsightNarrativeSlot(ctx, "2026-10-05", "en", "sleep", "new-day", "provider"); err != nil {
		t.Fatal(err)
	}
	if got := read("2026-10-05"); got.Insight.Text != "New accepted insight" {
		t.Fatal("invalidation erased retained answer")
	}
}

func TestLastGoodAIInsightAdoptsCompatibleCachedHistory(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := db.EnsureDailyInsightNarrativeSlotsTableContext(ctx); err != nil {
		t.Fatal(err)
	}
	// Existing accepted prose predates the new durable display history.
	payload, _ := json.Marshal(health.AIInsightSlotResponse{Version: health.AIInsightVersion, Locale: "en", Slot: "sleep", Insight: &health.AIInsightSection{Text: "Pre-release insight", Stance: "qualify"}})
	if _, err := db.pool.Exec(ctx, `INSERT INTO daily_insight_narrative_slots(date,lang,slot,material_input_hash,provider_fingerprint,narrative_input_hash,narrative,generation_state) VALUES ('2026-10-04','en','sleep','new','provider','old',$1,'cold')`, json.RawMessage(payload)); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetLastGoodAIInsights(ctx, "en", "2026-10-05")
	if err != nil || got["sleep"].Insight == nil || got["sleep"].Insight.Text != "Pre-release insight" || !got["sleep"].GeneratedAt.IsZero() {
		t.Fatalf("adoption invented time or lost history: %#v %v", got, err)
	}
	if _, err := db.pool.Exec(ctx, `DELETE FROM daily_insight_narrative_slots`); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetLastGoodAIInsights(ctx, "en", "2026-10-05")
	if err != nil || got["sleep"].Insight == nil {
		t.Fatalf("adopted history not durable: %#v %v", got, err)
	}
}
