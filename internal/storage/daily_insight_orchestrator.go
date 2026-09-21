package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"health-receiver/internal/ai"
	"health-receiver/internal/health"
)

const dailyInsightGenerationDeadline = 2 * time.Minute

// dailyInsightNarrativeDebounce gives an ingest burst time to settle before a
// provider request. The factual B0 snapshot is written and served before this
// timer starts; the delay applies only to optional B1 prose.
const dailyInsightNarrativeDebounce = 30 * time.Second

type dailyInsightNarrativeSlotWork struct {
	snapshot            *health.DailyInsightSnapshot
	aiCfg               AIConfig
	lang                string
	materialHash        string
	providerFingerprint string
	notBefore           time.Time
}

// dailyInsightNarrativeCoordinator serialises all provider work for one
// date/lang/overall key. Its pending item is deliberately replaced, rather
// than queued: only the newest exact material can become user-visible.
type dailyInsightNarrativeCoordinator struct {
	mu      sync.Mutex
	pending *dailyInsightNarrativeSlotWork
	wake    chan struct{}
	closed  bool
}

func newDailyInsightNarrativeCoordinator() *dailyInsightNarrativeCoordinator {
	return &dailyInsightNarrativeCoordinator{wake: make(chan struct{}, 1)}
}

// schedule returns whether work was accepted and whether this coordinator is
// still owned by the map. A caller that loses the idle-exit race retries with
// a fresh coordinator rather than putting pending work on a stopped worker.
func (c *dailyInsightNarrativeCoordinator) schedule(work dailyInsightNarrativeSlotWork) (scheduled, open bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return false, false
	}
	if c.pending != nil && c.pending.materialHash == work.materialHash && c.pending.providerFingerprint == work.providerFingerprint {
		c.mu.Unlock()
		return false, true
	}
	c.pending = &work
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
	return true, true
}

// takePendingIfDue atomically checks the latest pending item against its own
// quiet-window deadline. In particular, a replacement that arrives just as an
// older timer fires cannot inherit that older item's already-expired timer.
func (c *dailyInsightNarrativeCoordinator) takePendingIfDue(now time.Time) *dailyInsightNarrativeSlotWork {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil || c.pending.notBefore.After(now) {
		return nil
	}
	work := *c.pending
	c.pending = nil
	return &work
}

func (c *dailyInsightNarrativeCoordinator) currentPending() *dailyInsightNarrativeSlotWork {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		return nil
	}
	work := *c.pending
	return &work
}

// closeDailyInsightNarrativeCoordinatorIfIdle establishes closed while holding
// the same mutex used by schedule, then removes this exact coordinator before
// releasing it. A scheduler which loaded the old pointer either got in first
// (and pending is non-empty) or sees closed and retries LoadOrStore; no work
// can be accepted after the worker exits.
func (s *DB) closeDailyInsightNarrativeCoordinatorIfIdle(key string, coordinator *dailyInsightNarrativeCoordinator) bool {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if coordinator.pending != nil || coordinator.closed {
		return false
	}
	coordinator.closed = true
	s.dailyInsightInFlight.CompareAndDelete(key, coordinator)
	return true
}

func (s *DB) dailyInsightNow() time.Time {
	if s.dailyInsightNowFn != nil {
		return s.dailyInsightNowFn()
	}
	return time.Now()
}

func (s *DB) dailyInsightDebounceWindow() time.Duration {
	if s.dailyInsightDebounceFn > 0 {
		return s.dailyInsightDebounceFn
	}
	return dailyInsightNarrativeDebounce
}

// EnsureDailyInsightNarrativeSlotsAsync schedules only the overall B1
// synthesis. A material change is persisted as cold immediately, then waits
// for thirty seconds of quiet before one provider request. The in-memory
// coordinator is a local cost/control guard; the durable lease remains the
// cross-process authority.
func (s *DB) EnsureDailyInsightNarrativeSlotsAsync(snapshot *health.DailyInsightSnapshot, aiCfg AIConfig, lang string) bool {
	if snapshot == nil || !TodayInsightsB1GenerationEnabled(s, aiCfg) {
		return false
	}
	const slot = health.DailyInsightNarrativeOverallSlot
	if snapshot.Date != s.dailyInsightNow().In(s.reportTZLocation()).Format("2006-01-02") {
		// Today Insights is current-day only. In particular, a cache read of a
		// historical artifact must never schedule provider work.
		return false
	}
	if !health.HasEligibleDailyInsightNarrativeSlot(snapshot, lang, slot) {
		return false
	}
	providerFingerprint := DailyInsightGenerationFingerprint(aiCfg, lang)
	materialHash := health.DailyInsightNarrativeSlotMaterialHash(snapshot, lang, slot)
	if materialHash == "" {
		return false
	}
	// Ingestion can refresh a snapshot before the first HTTP read creates its
	// slot row. Persisting it first also makes prior prose unreadable at once.
	if err := s.UpsertDailyInsightNarrativeSlot(context.Background(), DailyInsightNarrativeSlot{
		Date: snapshot.Date, Lang: lang, Slot: slot, MaterialInputHash: materialHash, ProviderFingerprint: providerFingerprint,
	}); err != nil {
		log.Printf("daily insight narrative slot: initialize date=%s lang=%s slot=%s: %v", snapshot.Date, lang, slot, err)
		return false
	}
	entries, err := s.GetDailyInsightNarrativeSlots(snapshot.Date, lang)
	if err != nil {
		log.Printf("daily insight narrative slot: read date=%s lang=%s: %v", snapshot.Date, lang, err)
		return false
	}
	if entry, found := entries[slot]; found && entry.MaterialInputHash == materialHash &&
		entry.ProviderFingerprint == providerFingerprint && entry.NarrativeInputHash == materialHash &&
		entry.GenerationState == DailyInsightStateReady && len(entry.Narrative) > 0 {
		return false
	}
	key := snapshot.Date + "|" + lang + "|" + slot
	work := dailyInsightNarrativeSlotWork{
		snapshot: snapshot, aiCfg: aiCfg, lang: lang, materialHash: materialHash, providerFingerprint: providerFingerprint,
		notBefore: s.dailyInsightNow().Add(s.dailyInsightDebounceWindow()),
	}
	for {
		created := newDailyInsightNarrativeCoordinator()
		actual, loaded := s.dailyInsightInFlight.LoadOrStore(key, created)
		coordinator, ok := actual.(*dailyInsightNarrativeCoordinator)
		if !ok {
			log.Printf("daily insight narrative slot: invalid in-flight coordinator for %s", key)
			return false
		}
		scheduled, open := coordinator.schedule(work)
		if !open {
			// The runner removed this exact coordinator while this request was
			// acquiring its mutex. Retry with the map's current owner.
			continue
		}
		if !loaded {
			go s.runDailyInsightNarrativeCoordinator(key, coordinator)
		}
		return scheduled || !loaded
	}
}

func (s *DB) runDailyInsightNarrativeCoordinator(key string, coordinator *dailyInsightNarrativeCoordinator) {
	for {
		work := coordinator.currentPending()
		if work == nil {
			if s.closeDailyInsightNarrativeCoordinatorIfIdle(key, coordinator) {
				return
			}
			continue
		}
		wait := work.notBefore.Sub(s.dailyInsightNow())
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-coordinator.wake:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				continue
			}
		}
		// A new schedule may have landed while the timer fired. Take the latest
		// item only if *its own* quiet window elapsed; otherwise leave it pending
		// and restart the wait from that newer deadline.
		work = coordinator.takePendingIfDue(s.dailyInsightNow())
		if work == nil {
			continue
		}
		if err := s.EnsureDailyInsightNarrativeSlot(context.Background(), work.snapshot, work.aiCfg, work.lang, health.DailyInsightNarrativeOverallSlot, work.materialHash, work.providerFingerprint); err != nil {
			log.Printf("daily insight narrative slot: date=%s lang=%s slot=overall: %v", work.snapshot.Date, work.lang, err)
		}
	}
}

// EnsureDailyInsightNarrativeSlot generates one text overlay for a current
// snapshot. It is deliberately unable to persist or invalidate sibling slots.
func (s *DB) EnsureDailyInsightNarrativeSlot(ctx context.Context, snapshot *health.DailyInsightSnapshot, aiCfg AIConfig, lang, slot, materialHash, providerFingerprint string) error {
	input, known := health.BuildDailyInsightNarrativeSlotInput(snapshot, lang, slot)
	if !known || snapshot == nil || snapshot.Date == "" || materialHash == "" || providerFingerprint == "" {
		return fmt.Errorf("invalid daily insight narrative slot generation input")
	}
	if slot != health.DailyInsightNarrativeOverallSlot || !TodayInsightsB1GenerationEnabled(s, aiCfg) || !health.HasEligibleDailyInsightNarrativeSlot(snapshot, lang, slot) {
		return nil
	}
	if snapshot.Date != s.dailyInsightNow().In(s.reportTZLocation()).Format("2006-01-02") {
		return fmt.Errorf("refusing non-current daily insight slot generation for %s", snapshot.Date)
	}
	provider, active, err := ResolveTodayInsightsB1ProviderConfig(aiCfg)
	if err != nil {
		return err
	}
	leaseToken, err := s.ClaimDailyInsightNarrativeSlotGeneration(ctx, snapshot.Date, lang, slot, materialHash, providerFingerprint, s.dailyInsightNow())
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
		_, failErr := s.FailDailyInsightNarrativeSlotGeneration(ctx, snapshot.Date, lang, slot, materialHash, providerFingerprint, leaseToken, s.dailyInsightNow())
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
		_, failErr := s.FailDailyInsightNarrativeSlotGeneration(ctx, snapshot.Date, lang, slot, materialHash, providerFingerprint, leaseToken, s.dailyInsightNow())
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
