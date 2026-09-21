package storage

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

func TestDailyInsightLeaseCoversGenerationDeadline(t *testing.T) {
	if dailyInsightLeaseDuration <= dailyInsightGenerationDeadline {
		t.Fatalf("lease %s must exceed generation deadline %s", dailyInsightLeaseDuration, dailyInsightGenerationDeadline)
	}
}

const testNarrativeSlotProviderID = "storage-test-narrative-slot"

type testNarrativeSlotProvider struct{}

var (
	testNarrativeSlotProviderMu sync.Mutex
	testNarrativeSlotGenerate   func(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error)
)

func (testNarrativeSlotProvider) Descriptor() ai.ProviderDescriptor {
	return ai.ProviderDescriptor{ID: testNarrativeSlotProviderID, DisplayName: "Storage test", DefaultModel: "test-slot-model"}
}

func (testNarrativeSlotProvider) ListModels(context.Context, string) ([]ai.Model, error) {
	return nil, nil
}

func (testNarrativeSlotProvider) Generate(ctx context.Context, cfg ai.ProviderConfig, request ai.GenerationRequest) (ai.GenerationResult, error) {
	testNarrativeSlotProviderMu.Lock()
	generate := testNarrativeSlotGenerate
	testNarrativeSlotProviderMu.Unlock()
	if generate != nil {
		return generate(ctx, cfg, request)
	}
	return ai.GenerationResult{}, errors.New("test provider intentionally does not generate")
}

func init() {
	ai.RegisterProvider(testNarrativeSlotProvider{})
}

func setTestNarrativeSlotGenerate(t *testing.T, generate func(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error)) {
	t.Helper()
	testNarrativeSlotProviderMu.Lock()
	testNarrativeSlotGenerate = generate
	testNarrativeSlotProviderMu.Unlock()
	t.Cleanup(func() {
		testNarrativeSlotProviderMu.Lock()
		testNarrativeSlotGenerate = nil
		testNarrativeSlotProviderMu.Unlock()
	})
}

// overrideTestDailyInsightDebounce returns a restoration function. testDB holds
// sharedFullDBMu until its cleanup runs, so callers defer this result after the
// DB cleanup defer and restore the shared test DB state before that unlock.
func overrideTestDailyInsightDebounce(t *testing.T, db *DB, duration time.Duration) func() {
	t.Helper()
	previous := db.dailyInsightDebounceFn
	db.dailyInsightDebounceFn = duration
	return func() {
		db.dailyInsightDebounceFn = previous
	}
}

func approvedNarrativeSlotConfig(t *testing.T, db *DB) AIConfig {
	t.Helper()
	identity := ai.DailyInsightNarrativeSlotCurrentReviewIdentity()
	if err := db.SaveTodayInsightsB1QualityGateApproval(TodayInsightsB1QualityGateApproval{
		Version: TodayInsightsB1QualityGateVersion, CorpusHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Provider: testNarrativeSlotProviderID, Model: "test-slot-model", MaxOutputTokens: ai.DailyInsightMaxTokens,
		PromptRevision: identity.PromptRevision, ClaimPacketVersion: identity.ClaimPacketVersion,
		NarrativeVersion: identity.NarrativeVersion, ReviewFingerprint: identity.Fingerprint,
		ApprovedAt: "2026-09-17T12:00:00Z",
	}); err != nil {
		t.Fatalf("save B1 approval: %v", err)
	}
	if err := db.SaveSettings(map[string]string{SettingTodayInsightsB1Enabled: "true"}); err != nil {
		t.Fatalf("enable B1: %v", err)
	}
	return AIConfig{Provider: testNarrativeSlotProviderID, Providers: map[string]AIProviderSettings{
		testNarrativeSlotProviderID: {APIKey: "test-key", Model: "test-slot-model"},
	}}
}

func eligibleOverallSnapshot(db *DB, suffix string) *health.DailyInsightSnapshot {
	return &health.DailyInsightSnapshot{
		Date: db.dailyInsightNow().In(db.reportTZLocation()).Format("2006-01-02"),
		NarrativeFacts: []health.DailyInsightNarrativeFact{
			{ID: "sleep_" + suffix, Domain: "sleep", Statement: "Sleep fact " + suffix, Fresh: true, Authority: "server_derived"},
			{ID: "recovery_" + suffix, Domain: "recovery", Statement: "Recovery fact " + suffix, Fresh: true, Authority: "server_derived"},
		},
	}
}

func successfulEmptyOverallNarrative() ai.GenerationResult {
	return ai.GenerationResult{Text: `{"version":"today-insight-synthesis-v4","locale":"en","slot":{"key":"overall","section":null}}`}
}

func waitForNarrativeProviderCalls(t *testing.T, calls *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() >= want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("provider calls = %d, want at least %d", calls.Load(), want)
}

func waitForNarrativeFailure(t *testing.T, db *DB, date string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		entries, err := db.GetDailyInsightNarrativeSlots(date, "en")
		if err != nil {
			t.Fatalf("read narrative slot: %v", err)
		}
		entry := entries[health.DailyInsightNarrativeOverallSlot]
		if entry.GenerationState == DailyInsightStateFailed && entry.RetryAfter != nil && entry.RetryAfter.After(time.Now()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("failure did not record retry backoff")
}

func waitForNarrativeReady(t *testing.T, db *DB, date, materialHash string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		entries, err := db.GetDailyInsightNarrativeSlots(date, "en")
		if err != nil {
			t.Fatalf("read narrative slot: %v", err)
		}
		entry := entries[health.DailyInsightNarrativeOverallSlot]
		if entry.GenerationState == DailyInsightStateReady && entry.MaterialInputHash == materialHash && entry.NarrativeInputHash == materialHash {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("latest narrative did not become ready for material %q", materialHash)
}

func TestOverallCoordinatorResetsQuietWindowOnlyForNewMaterial(t *testing.T) {
	coordinator := newDailyInsightNarrativeCoordinator()
	base := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	first := dailyInsightNarrativeSlotWork{materialHash: "first", providerFingerprint: "provider", notBefore: base.Add(30 * time.Second)}
	if scheduled, open := coordinator.schedule(first); !scheduled || !open {
		t.Fatal("first work was not scheduled")
	}
	if scheduled, open := coordinator.schedule(dailyInsightNarrativeSlotWork{materialHash: "first", providerFingerprint: "provider", notBefore: base.Add(60 * time.Second)}); scheduled || !open {
		t.Fatal("same material reset the quiet window")
	}
	if got := coordinator.currentPending(); got == nil || !got.notBefore.Equal(first.notBefore) {
		t.Fatalf("same material changed pending work: %#v", got)
	}
	second := dailyInsightNarrativeSlotWork{materialHash: "second", providerFingerprint: "provider", notBefore: base.Add(60 * time.Second)}
	if scheduled, open := coordinator.schedule(second); !scheduled || !open {
		t.Fatal("new material did not reset the quiet window")
	}
	if got := coordinator.takePendingIfDue(second.notBefore); got == nil || got.materialHash != second.materialHash || !got.notBefore.Equal(second.notBefore) {
		t.Fatalf("newest pending work = %#v, want %#v", got, second)
	}
}

func TestOverallCoordinatorBoundaryReplacementUsesNewQuietWindow(t *testing.T) {
	coordinator := newDailyInsightNarrativeCoordinator()
	base := time.Date(2026, 9, 21, 10, 0, 0, 0, time.UTC)
	old := dailyInsightNarrativeSlotWork{materialHash: "old", providerFingerprint: "provider", notBefore: base}
	if scheduled, open := coordinator.schedule(old); !scheduled || !open {
		t.Fatal("old work was not scheduled")
	}
	// This models the boundary between an old timer firing and the worker's
	// atomic take. The newest item must not inherit the old deadline.
	newer := dailyInsightNarrativeSlotWork{materialHash: "new", providerFingerprint: "provider", notBefore: base.Add(30 * time.Second)}
	if scheduled, open := coordinator.schedule(newer); !scheduled || !open {
		t.Fatal("replacement work was not scheduled")
	}
	if got := coordinator.takePendingIfDue(base); got != nil {
		t.Fatalf("replacement ran on the old deadline: %#v", got)
	}
	if got := coordinator.currentPending(); got == nil || got.materialHash != newer.materialHash {
		t.Fatalf("replacement was lost at the old deadline: %#v", got)
	}
	if got := coordinator.takePendingIfDue(newer.notBefore); got == nil || got.materialHash != newer.materialHash {
		t.Fatalf("replacement did not run on its own deadline: %#v", got)
	}
}

func TestOverallCoordinatorIdleExitRejectsOldPointerAndAllowsReschedule(t *testing.T) {
	db := &DB{}
	const key = "2026-09-21|en|overall"
	old := newDailyInsightNarrativeCoordinator()
	db.dailyInsightInFlight.Store(key, old)
	if !db.closeDailyInsightNarrativeCoordinatorIfIdle(key, old) {
		t.Fatal("idle coordinator was not closed")
	}
	if _, found := db.dailyInsightInFlight.Load(key); found {
		t.Fatal("closed coordinator remained in the in-flight map")
	}
	if scheduled, open := old.schedule(dailyInsightNarrativeSlotWork{materialHash: "old"}); scheduled || open {
		t.Fatalf("closed coordinator accepted work: scheduled=%v open=%v", scheduled, open)
	}
	newCoordinator := newDailyInsightNarrativeCoordinator()
	actual, loaded := db.dailyInsightInFlight.LoadOrStore(key, newCoordinator)
	if loaded || actual != newCoordinator {
		t.Fatalf("reschedule did not get a fresh coordinator: actual=%#v loaded=%v", actual, loaded)
	}
	if scheduled, open := newCoordinator.schedule(dailyInsightNarrativeSlotWork{materialHash: "new"}); !scheduled || !open {
		t.Fatalf("fresh coordinator rejected work: scheduled=%v open=%v", scheduled, open)
	}
}

func TestOverallRuntimeAllocatesOnlyOverallLifecycleRow(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	restoreDebounce := overrideTestDailyInsightDebounce(t, db, 10*time.Millisecond)
	defer restoreDebounce()
	config := approvedNarrativeSlotConfig(t, db)
	var calls atomic.Int32
	setTestNarrativeSlotGenerate(t, func(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error) {
		calls.Add(1)
		return ai.GenerationResult{}, errors.New("test provider failure")
	})
	snapshot := eligibleOverallSnapshot(db, "overall")
	if !db.EnsureDailyInsightNarrativeSlotsAsync(snapshot, config, "en") {
		t.Fatal("eligible overall synthesis did not schedule a narrative worker")
	}
	entries, err := db.GetDailyInsightNarrativeSlots(snapshot.Date, "en")
	if err != nil {
		t.Fatalf("read lifecycle entries: %v", err)
	}
	if _, found := entries[health.DailyInsightNarrativeOverallSlot]; !found {
		t.Fatalf("eligible overall synthesis did not allocate its lifecycle row: %#v", entries)
	}
	if len(entries) != 1 {
		t.Fatalf("non-overall slots allocated lifecycle rows: %#v", entries)
	}
	// Let the intentionally failing worker finish before this test's DB is
	// closed, so its configured provider cannot leak into the next test.
	waitForNarrativeProviderCalls(t, &calls, 1)
	waitForNarrativeFailure(t, db, snapshot.Date)
}

func TestOverallSchedulerCollapsesBurstAndRunsLatestOnly(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	restoreDebounce := overrideTestDailyInsightDebounce(t, db, 10*time.Millisecond)
	defer restoreDebounce()
	config := approvedNarrativeSlotConfig(t, db)
	var calls atomic.Int32
	setTestNarrativeSlotGenerate(t, func(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error) {
		calls.Add(1)
		return successfulEmptyOverallNarrative(), nil
	})
	first := eligibleOverallSnapshot(db, "first")
	second := eligibleOverallSnapshot(db, "second")
	third := eligibleOverallSnapshot(db, "third")
	if !db.EnsureDailyInsightNarrativeSlotsAsync(first, config, "en") || !db.EnsureDailyInsightNarrativeSlotsAsync(second, config, "en") || !db.EnsureDailyInsightNarrativeSlotsAsync(third, config, "en") {
		t.Fatal("eligible overall burst was not scheduled")
	}
	waitForNarrativeProviderCalls(t, &calls, 1)
	waitForNarrativeReady(t, db, third.Date, health.DailyInsightNarrativeSlotMaterialHash(third, "en", health.DailyInsightNarrativeOverallSlot))
	time.Sleep(25 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls after collapsed burst = %d, want 1", got)
	}
	entries, err := db.GetDailyInsightNarrativeSlots(third.Date, "en")
	if err != nil {
		t.Fatalf("read narrative slot: %v", err)
	}
	entry := entries[health.DailyInsightNarrativeOverallSlot]
	if got, want := entry.MaterialInputHash, health.DailyInsightNarrativeSlotMaterialHash(third, "en", health.DailyInsightNarrativeOverallSlot); got != want {
		t.Fatalf("material hash = %q, want latest %q", got, want)
	}
}

func TestOverallSchedulerSerializesLatestWorkAndRejectsStaleSave(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	restoreDebounce := overrideTestDailyInsightDebounce(t, db, time.Nanosecond)
	defer restoreDebounce()
	config := approvedNarrativeSlotConfig(t, db)
	entered := make(chan struct{}, 2)
	releaseFirst := make(chan struct{})
	var calls, active, maxActive atomic.Int32
	setTestNarrativeSlotGenerate(t, func(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error) {
		call := calls.Add(1)
		current := active.Add(1)
		for {
			seen := maxActive.Load()
			if current <= seen || maxActive.CompareAndSwap(seen, current) {
				break
			}
		}
		entered <- struct{}{}
		if call == 1 {
			<-releaseFirst
		}
		active.Add(-1)
		return successfulEmptyOverallNarrative(), nil
	})
	first := eligibleOverallSnapshot(db, "first")
	second := eligibleOverallSnapshot(db, "second")
	if !db.EnsureDailyInsightNarrativeSlotsAsync(first, config, "en") {
		t.Fatal("first overall was not scheduled")
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("first provider call did not start")
	}
	if !db.EnsureDailyInsightNarrativeSlotsAsync(second, config, "en") {
		t.Fatal("latest overall was not scheduled")
	}
	close(releaseFirst)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("latest provider call did not start")
	}
	if got := maxActive.Load(); got != 1 {
		t.Fatalf("max concurrent provider calls = %d, want 1", got)
	}
	waitForNarrativeProviderCalls(t, &calls, 2)
	waitForNarrativeReady(t, db, second.Date, health.DailyInsightNarrativeSlotMaterialHash(second, "en", health.DailyInsightNarrativeOverallSlot))
	entries, err := db.GetDailyInsightNarrativeSlots(second.Date, "en")
	if err != nil {
		t.Fatalf("read narrative slot: %v", err)
	}
	entry := entries[health.DailyInsightNarrativeOverallSlot]
	if entry.GenerationState != DailyInsightStateReady || entry.NarrativeInputHash != entry.MaterialInputHash {
		t.Fatalf("latest work did not become ready: %#v", entry)
	}
}

func TestOverallSchedulerHonorsFailureBackoff(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	restoreDebounce := overrideTestDailyInsightDebounce(t, db, time.Nanosecond)
	defer restoreDebounce()
	config := approvedNarrativeSlotConfig(t, db)
	var calls atomic.Int32
	setTestNarrativeSlotGenerate(t, func(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error) {
		calls.Add(1)
		return ai.GenerationResult{}, errors.New("test provider failure")
	})
	snapshot := eligibleOverallSnapshot(db, "failure")
	if !db.EnsureDailyInsightNarrativeSlotsAsync(snapshot, config, "en") {
		t.Fatal("failing overall was not scheduled")
	}
	waitForNarrativeProviderCalls(t, &calls, 1)
	waitForNarrativeFailure(t, db, snapshot.Date)
	// A poll may schedule housekeeping again, but Claim remains blocked by the
	// durable retry_after; no second provider call is allowed during backoff.
	db.EnsureDailyInsightNarrativeSlotsAsync(snapshot, config, "en")
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("provider calls during failure backoff = %d, want 1", got)
	}
}

func TestOverallSchedulerRefusesHistoricalProviderWork(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	config := approvedNarrativeSlotConfig(t, db)
	var calls atomic.Int32
	setTestNarrativeSlotGenerate(t, func(context.Context, ai.ProviderConfig, ai.GenerationRequest) (ai.GenerationResult, error) {
		calls.Add(1)
		return successfulEmptyOverallNarrative(), nil
	})
	snapshot := eligibleOverallSnapshot(db, "historical")
	snapshot.Date = "2000-01-01"
	if db.EnsureDailyInsightNarrativeSlotsAsync(snapshot, config, "en") {
		t.Fatal("historical snapshot scheduled provider work")
	}
	time.Sleep(15 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("historical provider calls = %d, want 0", got)
	}
}

func TestTodayInsightsB1PreviewIsTenantLocalAndNotAnApproval(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	config := AIConfig{Provider: testNarrativeSlotProviderID, Providers: map[string]AIProviderSettings{
		testNarrativeSlotProviderID: {APIKey: "test-key", Model: "test-slot-model"},
	}}
	if err := db.SaveSettings(map[string]string{SettingTodayInsightsB1PreviewEnabled: "true"}); err != nil {
		t.Fatalf("enable B1 preview: %v", err)
	}
	if TodayInsightsB1Enabled(db) {
		t.Fatal("tenant preview must not set the approved B1 release flag")
	}
	if TodayInsightsB1ApprovedForConfig(db, config) {
		t.Fatal("tenant preview must not create a quality-gate approval")
	}
	if got := TodayInsightsB1NarrativeMode(db, config); got != TodayInsightsB1NarrativeModePreview {
		t.Fatalf("B1 narrative mode = %q, want preview", got)
	}
	if !TodayInsightsB1GenerationEnabled(db, config) {
		t.Fatal("tenant preview did not permit its own B1 generation")
	}
	if err := db.SaveSettings(map[string]string{SettingTodayInsightsB1PreviewEnabled: "false"}); err != nil {
		t.Fatalf("disable B1 preview: %v", err)
	}
	if got := TodayInsightsB1NarrativeMode(db, config); got != TodayInsightsB1NarrativeModeDisabled {
		t.Fatalf("B1 narrative mode after disable = %q, want disabled", got)
	}
}

func TestDailyInsightGenerationFingerprintIncludesStaticReviewIdentity(t *testing.T) {
	cfg := AIConfig{Provider: "openai", Providers: map[string]AIProviderSettings{
		"openai": {APIKey: "test-key", Model: "gpt-5.6-luna", ReasoningEffort: "none"},
	}}
	identity := ai.DailyInsightNarrativeSlotCurrentReviewIdentity()
	got := DailyInsightGenerationFingerprint(cfg, "en")
	want := ai.HashForGeneration("", ai.GenerationFingerprint{
		Provider: "openai", Model: "gpt-5.6-luna", ReasoningEffort: "none", MaxOutputTokens: ai.DailyInsightMaxTokens,
		PromptRevision: identity.PromptRevision + "|" + identity.Fingerprint + "|" + health.DailyInsightSnapshotVersion + "|" + health.DailyInsightPolicyVersion + "|" + health.DailyInsightActionCatalogVersion + "|en",
	})
	if got != want {
		t.Fatalf("generation fingerprint = %q, want identity-bound fingerprint %q", got, want)
	}
	if got == ai.HashForGeneration("", ai.GenerationFingerprint{
		Provider: "openai", Model: "gpt-5.6-luna", ReasoningEffort: "none", MaxOutputTokens: ai.DailyInsightMaxTokens,
		PromptRevision: health.DailyInsightPromptRevision + "|" + health.DailyInsightNarrativeInputVersion + "|" + health.DailyInsightNarrativeVersion + "|" + health.DailyInsightSnapshotVersion + "|" + health.DailyInsightPolicyVersion + "|" + health.DailyInsightActionCatalogVersion + "|en",
	}) {
		t.Fatal("generation fingerprint retained the legacy version-only prompt key")
	}
}

func TestDailyInsightNarrativeStaticRevisionMatchesReviewIdentity(t *testing.T) {
	identity := ai.DailyInsightNarrativeSlotCurrentReviewIdentity()
	if got, want := DailyInsightNarrativeStaticRevision(), identity.PromptRevision+"|"+identity.Fingerprint; got != want {
		t.Fatalf("stored static revision = %q, want %q", got, want)
	}
}
