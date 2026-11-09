package storage

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

func TestAIInsightGenerationStatusIgnoresSupersededWorker(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := db.UpsertDailyInsightNarrativeSlot(ctx, DailyInsightNarrativeSlot{Date: db.Today(), Lang: "en", Slot: "energy", MaterialInputHash: "current", ProviderFingerprint: "fingerprint"}); err != nil {
		t.Fatal(err)
	}
	status := AIGenerationStatus{Pipeline: "today", Date: db.Today(), Lang: "en", Slot: "energy", MaterialHash: "old-material", ProviderFingerprint: "fingerprint", State: DailyInsightStateReady, ReasonCode: "ready"}
	if err := db.saveAIGenerationStatus(ctx, status); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetAIGenerationStatuses(ctx, db.Today(), "en")
	if err != nil || len(got) != 0 {
		t.Fatalf("superseded worker status leaked: %+v %v", got, err)
	}
	status.MaterialHash = "current"
	if err := db.saveAIGenerationStatus(ctx, status); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetAIGenerationStatuses(ctx, db.Today(), "en")
	if err != nil || len(got) != 1 || got[0].ReasonCode != "ready" {
		t.Fatalf("current status missing: %+v %v", got, err)
	}
}

func TestAIInsightBackgroundDiagnosticsDoNotBlockWorker(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	if err := db.SaveSettings(map[string]string{SettingTodayInsightsB1PreviewEnabled: "true", "timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.pool.Exec(ctx, `DROP TABLE ai_generation_status`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.EnsureAIGenerationStatusTableContext(context.Background()); err != nil {
			t.Errorf("restore diagnostics table: %v", err)
		}
	}()
	restore := overrideTestDailyInsightDebounce(t, db, time.Millisecond)
	defer restore()
	var calls atomic.Int32
	setTestNarrativeSlotGenerate(t, func(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error) {
		calls.Add(1)
		return ai.GenerationResult{}, errors.New("synthetic failure")
	})
	cfg := AIConfig{Provider: testNarrativeSlotProviderID, Providers: map[string]AIProviderSettings{testNarrativeSlotProviderID: {APIKey: "synthetic-only"}}}
	snapshot := &health.DailyInsightSnapshot{Date: db.Today(), Primary: health.DailyInsight{State: "insight"}, Domains: []health.DailyInsightDomain{{Key: "sleep", DataState: "fresh", Confidence: "final", Insight: health.DailyInsight{State: "insight", Observation: "Observed sleep"}}}, NarrativeFacts: []health.DailyInsightNarrativeFact{{ID: "sleep_fact", Domain: "sleep", Fresh: true, Statement: "Sleep is observed"}}}
	if err := db.ScheduleSnapshotAIInsights(ctx, snapshot, cfg, "en", "test"); err != nil {
		t.Fatalf("diagnostics blocked scheduling: %v", err)
	}
	waitForNarrativeProviderCalls(t, &calls, 1)
	deadline := time.Now().Add(2 * time.Second)
	for {
		entries, err := db.GetDailyInsightNarrativeSlots(snapshot.Date, "en")
		if err != nil {
			t.Fatal(err)
		}
		if entries["sleep"].GenerationState == DailyInsightStateFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not finish independently of diagnostics")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAIInsightBackgroundRefreshRunsWithoutRequestAndHonorsBackoff(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	if err := db.SaveSettings(map[string]string{"today_insights_b1_preview_enabled": "true", "timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	restore := overrideTestDailyInsightDebounce(t, db, time.Millisecond)
	defer restore()
	cfg := AIConfig{Provider: testNarrativeSlotProviderID, Providers: map[string]AIProviderSettings{testNarrativeSlotProviderID: {APIKey: "synthetic-only"}}}
	var calls atomic.Int32
	setTestNarrativeSlotGenerate(t, func(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error) {
		calls.Add(1)
		return ai.GenerationResult{}, errors.New("synthetic provider failure")
	})
	snapshot := &health.DailyInsightSnapshot{Date: db.Today(), Primary: health.DailyInsight{State: "insight", Observation: "Observed context"}, Domains: []health.DailyInsightDomain{{Key: "sleep", DataState: "fresh", Confidence: "final", Insight: health.DailyInsight{State: "insight", Observation: "Observed sleep"}}}, NarrativeFacts: []health.DailyInsightNarrativeFact{{ID: "sleep_fact", Domain: "sleep", Fresh: true, Statement: "Sleep is observed"}}}
	if err := db.ScheduleSnapshotAIInsights(context.Background(), snapshot, cfg, "en", "derived_refresh"); err != nil {
		t.Fatal(err)
	}
	waitForNarrativeProviderCalls(t, &calls, 1)
	deadline := time.Now().Add(2 * time.Second)
	for {
		entries, err := db.GetDailyInsightNarrativeSlots(snapshot.Date, "en")
		if err != nil {
			t.Fatal(err)
		}
		if entries["sleep"].GenerationState == DailyInsightStateFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("background worker did not persist failure")
		}
		time.Sleep(time.Millisecond)
	}
	if err := db.ScheduleSnapshotAIInsights(context.Background(), snapshot, cfg, "en", "lifecycle"); err != nil {
		t.Fatal(err)
	}
	statuses, err := db.GetAIGenerationStatuses(context.Background(), snapshot.Date, "en")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, status := range statuses {
		if status.Slot == "sleep" {
			found = status.ReasonCode == "backoff" && status.LastAttemptAt != nil
		}
	}
	if !found {
		t.Fatalf("missing durable backoff diagnostics: %#v", statuses)
	}
	if calls.Load() != 1 {
		t.Fatal("backoff retried provider")
	}
	snapshot.Date = "2000-01-01"
	if err := db.ScheduleSnapshotAIInsights(context.Background(), snapshot, cfg, "en", "history"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("historical refresh called provider")
	}
}

func TestAIInsightBackgroundNullIsTerminalAndOverallWaitsForDomains(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	if err := db.SaveSettings(map[string]string{"today_insights_b1_preview_enabled": "true", "timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	restore := overrideTestDailyInsightDebounce(t, db, time.Millisecond)
	defer restore()
	cfg := AIConfig{Provider: testNarrativeSlotProviderID, Providers: map[string]AIProviderSettings{testNarrativeSlotProviderID: {APIKey: "synthetic-only"}}}
	var calls atomic.Int32
	setTestNarrativeSlotGenerate(t, func(_ context.Context, _ ai.ProviderConfig, request ai.GenerationRequest) (ai.GenerationResult, error) {
		calls.Add(1)
		var input health.AIInsightInput
		if err := json.Unmarshal(request.UserPayload, &input); err != nil {
			return ai.GenerationResult{}, err
		}
		payload, _ := json.Marshal(health.AIInsightSlotResponse{Version: health.AIInsightVersion, Locale: input.Locale, Slot: input.Slot})
		return ai.GenerationResult{Text: string(payload)}, nil
	})
	snapshot := &health.DailyInsightSnapshot{Date: db.Today(), Primary: health.DailyInsight{State: "insight", Observation: "Observed context"}, Domains: []health.DailyInsightDomain{{Key: "sleep", DataState: "fresh", Confidence: "final", Insight: health.DailyInsight{State: "insight", Observation: "Observed sleep"}}}, NarrativeFacts: []health.DailyInsightNarrativeFact{{ID: "sleep_fact", Domain: "sleep", Fresh: true, Statement: "Sleep is observed"}}}
	waitSlot := func(slot string) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for {
			entries, err := db.GetDailyInsightNarrativeSlots(snapshot.Date, "en")
			if err != nil {
				t.Fatal(err)
			}
			if entries[slot].GenerationState == DailyInsightStateReady {
				return
			}
			if time.Now().After(deadline) {
				t.Fatalf("slot %s not ready", slot)
			}
			time.Sleep(time.Millisecond)
		}
	}
	if err := db.ScheduleSnapshotAIInsights(context.Background(), snapshot, cfg, "en", "derived_refresh"); err != nil {
		t.Fatal(err)
	}
	entries, err := db.GetDailyInsightNarrativeSlots(snapshot.Date, "en")
	if err != nil {
		t.Fatal(err)
	}
	if _, found := entries["overall"]; found {
		t.Fatal("overall scheduled before domain terminal state")
	}
	waitSlot("sleep")
	if err := db.ScheduleSnapshotAIInsights(context.Background(), snapshot, cfg, "en", "lifecycle"); err != nil {
		t.Fatal(err)
	}
	waitSlot("overall")
	if err := db.ScheduleSnapshotAIInsights(context.Background(), snapshot, cfg, "en", "lifecycle"); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("intentional null should generate once per eligible slot: %d", calls.Load())
	}
	statuses, err := db.GetAIGenerationStatuses(context.Background(), snapshot.Date, "en")
	if err != nil {
		t.Fatal(err)
	}
	for _, status := range statuses {
		if status.Slot == "overall" || status.Slot == "sleep" {
			if status.ReasonCode != "no_added_value" {
				t.Fatalf("null not explained: %#v", status)
			}
		}
	}
}

func TestAIInsightBackgroundDoesNotReuseLegacyApproval(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	cfg := approvedNarrativeSlotConfig(t, db)
	if err := db.SaveSettings(map[string]string{SettingTodayInsightsB1PreviewEnabled: "false", "timezone": "UTC"}); err != nil {
		t.Fatal(err)
	}
	if TodayInsightsB1NarrativeMode(db, cfg) != TodayInsightsB1NarrativeModeApproved {
		t.Fatal("fixture did not establish approved mode")
	}
	restore := overrideTestDailyInsightDebounce(t, db, time.Millisecond)
	defer restore()
	var calls atomic.Int32
	setTestNarrativeSlotGenerate(t, func(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error) {
		calls.Add(1)
		return ai.GenerationResult{Text: `{"version":"today-ai-insight-v1","locale":"en","slot":"sleep","insight":null}`}, nil
	})
	snapshot := &health.DailyInsightSnapshot{Date: db.Today(), Primary: health.DailyInsight{State: "insight"}, Domains: []health.DailyInsightDomain{{Key: "sleep", DataState: "fresh", Confidence: "final", Insight: health.DailyInsight{State: "insight", Observation: "Observed sleep"}}}, NarrativeFacts: []health.DailyInsightNarrativeFact{{ID: "sleep_fact", Domain: "sleep", Fresh: true, Statement: "Recorded sleep observations"}}}
	if err := db.ScheduleSnapshotAIInsights(context.Background(), snapshot, cfg, "en", "lifecycle"); err != nil {
		t.Fatal(err)
	}
	input, eligible := health.BuildAIInsightInput(snapshot, "en", "sleep", nil)
	if !eligible {
		t.Fatal("fixture is not eligible")
	}
	if TodayAIInsightGenerationEnabled(db, cfg) || db.ScheduleAIInsightSlotAsync(snapshot, cfg, input) {
		t.Fatal("legacy narrative approval authorized independent opinions")
	}
	entries, err := db.GetDailyInsightNarrativeSlots(snapshot.Date, "en")
	if err != nil || len(entries) != 0 || calls.Load() != 0 {
		t.Fatalf("disabled pipeline created work: entries=%d calls=%d err=%v", len(entries), calls.Load(), err)
	}
	if err := db.SaveSettings(map[string]string{SettingTodayInsightsB1PreviewEnabled: "true"}); err != nil {
		t.Fatal(err)
	}
	if err := db.ScheduleSnapshotAIInsights(context.Background(), snapshot, cfg, "en", "lifecycle"); err != nil {
		t.Fatal(err)
	}
	waitForNarrativeProviderCalls(t, &calls, 1)
}

func TestAIInsightBackgroundReadsExactCachedOpinion(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	cfg := approvedNarrativeSlotConfig(t, db)
	if err := db.SaveSettings(map[string]string{SettingTodayInsightsB1PreviewEnabled: "true"}); err != nil {
		t.Fatal(err)
	}
	snapshot := &health.DailyInsightSnapshot{Date: db.Today(), Primary: health.DailyInsight{State: "insight"}, Domains: []health.DailyInsightDomain{{Key: "sleep", DataState: "fresh", Confidence: "final", Insight: health.DailyInsight{State: "insight", Observation: "Recorded sleep"}}}, NarrativeFacts: []health.DailyInsightNarrativeFact{{ID: "sleep_fact", Domain: "sleep", Fresh: true, Statement: "Sleep observations are recorded."}}}
	input, eligible := health.BuildAIInsightInput(snapshot, "en", "sleep", nil)
	if !eligible {
		t.Fatal("fixture is not eligible")
	}
	hash := health.AIInsightInputHash(input)
	fingerprint := AIInsightGenerationFingerprint(cfg, "en")
	if err := db.UpsertDailyInsightNarrativeSlot(ctx, DailyInsightNarrativeSlot{Date: snapshot.Date, Lang: "en", Slot: "sleep", MaterialInputHash: hash, ProviderFingerprint: fingerprint}); err != nil {
		t.Fatal(err)
	}
	token, err := db.ClaimDailyInsightNarrativeSlotGeneration(ctx, snapshot.Date, "en", "sleep", hash, fingerprint, time.Now())
	if err != nil || token == "" {
		t.Fatalf("lease %q %v", token, err)
	}
	body := json.RawMessage(`{"version":"today-ai-insight-v1","locale":"en","slot":"sleep","insight":{"text":"The recorded observations describe this sleep episode.","fact_ids":["sleep_fact"],"stance":"agree","alternative_action":""}}`)
	if saved, err := db.SaveDailyInsightNarrativeSlot(ctx, snapshot.Date, "en", "sleep", hash, fingerprint, token, body); err != nil || !saved {
		t.Fatalf("save %v %v", saved, err)
	}
	var calls atomic.Int32
	setTestNarrativeSlotGenerate(t, func(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error) {
		calls.Add(1)
		return ai.GenerationResult{}, errors.New("must remain read-only")
	})
	got, err := db.CurrentCachedAIInsight(ctx, snapshot, cfg, "en", "sleep")
	if err != nil || got == nil || got.Text != "The recorded observations describe this sleep episode." {
		t.Fatalf("cached opinion not loaded: %+v %v", got, err)
	}
	snapshot.NarrativeFacts[0].Statement = "Changed sleep observations."
	if got, err = db.CurrentCachedAIInsight(ctx, snapshot, cfg, "en", "sleep"); err != nil || got != nil {
		t.Fatal("old material rendered as current")
	}
	snapshot.NarrativeFacts[0].Statement = "Sleep observations are recorded."
	other := cfg
	other.Providers = map[string]AIProviderSettings{testNarrativeSlotProviderID: {APIKey: "test-key", Model: "different-model"}}
	if got, err = db.CurrentCachedAIInsight(ctx, snapshot, other, "en", "sleep"); err != nil || got != nil {
		t.Fatal("different provider fingerprint admitted")
	}
	if calls.Load() != 0 {
		t.Fatal("cache reader initiated provider work")
	}
}
