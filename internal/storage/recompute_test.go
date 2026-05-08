package storage

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// shortDebounce swaps in a tiny pause so tests don't sit on the
// production 2-second debounce. Restored via t.Cleanup.
func shortDebounce(t *testing.T) {
	t.Helper()
	prev := recomputeDebounce
	recomputeDebounce = 1 * time.Millisecond
	t.Cleanup(func() { recomputeDebounce = prev })
}

// waitFor polls cond until it returns true or the timeout elapses.
// Used instead of fixed sleeps so the tests don't flake on slow CI.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("condition not met within %v", timeout)
}

// TestTenantRecompute_RunsOnce: a single Trigger spawns exactly one
// worker that executes work once. Baseline behaviour.
func TestTenantRecompute_RunsOnce(t *testing.T) {
	shortDebounce(t)
	var rc TenantRecompute
	var calls atomic.Int32
	rc.Trigger(context.Background(), func() { calls.Add(1) })
	waitFor(t, time.Second, func() bool { return calls.Load() == 1 })
	// Give a beat for any rogue extra pass; should stay at 1.
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
}

// TestTenantRecompute_CollapsesBurst: many concurrent triggers fired
// while a worker is busy collapse into exactly one extra rerun (total
// of 2 work calls). The count MUST NOT scale with the trigger count —
// that's the entire point of the primitive.
func TestTenantRecompute_CollapsesBurst(t *testing.T) {
	shortDebounce(t)
	var rc TenantRecompute
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})

	work := func() {
		n := calls.Add(1)
		if n == 1 {
			// First pass: signal that we're inside, then block until
			// triggers have piled up. Subsequent passes (the rerun)
			// return immediately so the worker can drain.
			close(started)
			<-release
		}
	}

	rc.Trigger(context.Background(), work)
	<-started // worker is parked inside pass #1

	// Fire a burst of triggers while the worker is busy. They MUST
	// collapse into one dirty=true; the worker will execute exactly
	// one rerun after we release.
	const burst = 100
	var wg sync.WaitGroup
	wg.Add(burst)
	for i := 0; i < burst; i++ {
		go func() {
			defer wg.Done()
			rc.Trigger(context.Background(), work)
		}()
	}
	wg.Wait()

	close(release)
	waitFor(t, time.Second, func() bool { return calls.Load() >= 2 })
	// Drain any in-flight rerun.
	time.Sleep(20 * time.Millisecond)

	if got := calls.Load(); got != 2 {
		t.Fatalf("expected exactly 2 calls (initial + 1 collapsed rerun), got %d", got)
	}
}

// TestTenantRecompute_TriggerAfterIdle: once the worker exits, a fresh
// Trigger spawns a new worker. Without this, the primitive would only
// work for the very first trigger of the process's lifetime.
func TestTenantRecompute_TriggerAfterIdle(t *testing.T) {
	shortDebounce(t)
	var rc TenantRecompute
	var calls atomic.Int32

	rc.Trigger(context.Background(), func() { calls.Add(1) })
	waitFor(t, time.Second, func() bool { return calls.Load() == 1 })

	rc.Trigger(context.Background(), func() { calls.Add(1) })
	waitFor(t, time.Second, func() bool { return calls.Load() == 2 })
}

// TestTenantRecompute_NoLossOnUnlockRace: regression for the race
// between dirty.Load()=false and mu.Unlock(). A Trigger that fires in
// that window must not lose its recompute request. We use the
// preUnlockHook injection point to deterministically fire the racing
// Trigger at the exact moment the worker is about to exit.
func TestTenantRecompute_NoLossOnUnlockRace(t *testing.T) {
	shortDebounce(t)
	var rc TenantRecompute
	var calls atomic.Int32
	ctx := context.Background()

	// Restore on cleanup so other tests don't see a stale hook.
	t.Cleanup(func() { preUnlockHook.Store(nil) })

	var once atomic.Bool
	hook := func() {
		if !once.CompareAndSwap(false, true) {
			return
		}
		// Worker still holds the mutex here (hook runs BEFORE Unlock).
		// This Trigger will TryLock-fail and Store(dirty=true). Without
		// the recheck-after-unlock branch, the worker would Unlock and
		// return, losing this recompute request.
		rc.Trigger(ctx, func() { calls.Add(1) })
	}
	preUnlockHook.Store(&hook)

	rc.Trigger(ctx, func() { calls.Add(1) })

	// Expect: pass 1 + reacquire pass 2 = 2 calls. Without the fix,
	// only the initial pass runs and calls stays at 1.
	waitFor(t, time.Second, func() bool { return calls.Load() == 2 })
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 2 {
		t.Fatalf("unlock race lost a recompute: expected 2 calls, got %d", got)
	}
}

// TestTenantRecompute_ContextCancelStopsRerun: when ctx is cancelled
// while the worker is in the debounce window, it exits cleanly without
// running another pass. A pending dirty flag is dropped — recompute is
// event-driven, the next ingest after restart will re-trigger.
func TestTenantRecompute_ContextCancelStopsRerun(t *testing.T) {
	// Long debounce so the cancel reliably catches the worker mid-sleep.
	prev := recomputeDebounce
	recomputeDebounce = 500 * time.Millisecond
	t.Cleanup(func() { recomputeDebounce = prev })

	var rc TenantRecompute
	var calls atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())

	started := make(chan struct{})
	holdPass1 := make(chan struct{})
	work := func() {
		// Pass 1 holds the worker inside work() until the test has had
		// a chance to set dirty via a second Trigger. Without this gate
		// the worker can race to dirty.Load() before the test sets it,
		// completing pass 1 and starting a fresh pass 2 that bumps
		// calls to 2 — masking what the cancel was supposed to prevent.
		if calls.Add(1) == 1 {
			close(started)
			<-holdPass1
		}
	}

	rc.Trigger(ctx, work)
	<-started
	// Pile a trigger while pass 1 is parked → dirty=true.
	rc.Trigger(ctx, work)
	// Release pass 1; worker now checks dirty (true) and enters the
	// debounce select.
	close(holdPass1)
	// Cancel inside the debounce window. The select must pick ctx.Done
	// instead of time.After.
	time.Sleep(50 * time.Millisecond)
	cancel()

	// After the debounce window has fully elapsed, calls should still be
	// 1 — the rerun was preempted.
	time.Sleep(recomputeDebounce + 100*time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected worker to exit at 1 call, got %d", got)
	}
}

