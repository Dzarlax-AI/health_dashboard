package storage

import (
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
	rc.Trigger(func() { calls.Add(1) })
	waitFor(t, time.Second, func() bool { return calls.Load() == 1 })
	// Give a beat for any rogue extra pass; should stay at 1.
	time.Sleep(20 * time.Millisecond)
	if got := calls.Load(); got != 1 {
		t.Fatalf("expected 1 call, got %d", got)
	}
}

// TestTenantRecompute_CollapsesBurst: many concurrent triggers fired
// while a worker is busy collapse into at-most-one extra rerun. The
// exact count is implementation-defined (1 or 2 calls depending on
// timing) but MUST NOT scale with the trigger count.
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

	rc.Trigger(work)
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
			rc.Trigger(work)
		}()
	}
	wg.Wait()

	close(release)
	waitFor(t, time.Second, func() bool { return calls.Load() >= 2 })
	// Drain any in-flight rerun.
	time.Sleep(20 * time.Millisecond)

	got := calls.Load()
	if got < 2 || got > 2 {
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

	rc.Trigger(func() { calls.Add(1) })
	waitFor(t, time.Second, func() bool { return calls.Load() == 1 })

	rc.Trigger(func() { calls.Add(1) })
	waitFor(t, time.Second, func() bool { return calls.Load() == 2 })
}

