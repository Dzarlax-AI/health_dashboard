package storage

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// recomputeDebounce is the pause inserted between consecutive recompute
// passes when triggers fire while a worker is already running. The first
// pass after an idle state runs immediately; only repeats are debounced,
// so a burst of ingests collapses into one immediate compute + one
// follow-up that sees the latest data.
//
// Variable (not const) so tests can lower it without touching production
// code.
var recomputeDebounce = 2 * time.Second

// preUnlockHook is invoked by the worker after observing dirty=false
// but BEFORE releasing the mutex. Test-only injection point used to
// deterministically reproduce the unlock-race (a Trigger that fires
// between our dirty.Load and our Unlock must not lose its recompute
// request). Production code leaves this nil. atomic.Pointer so test
// setup/cleanup writes don't race the worker's read.
var preUnlockHook atomic.Pointer[func()]

// TenantRecompute serialises event-driven recompute work for a single
// tenant. At most one worker goroutine runs at a time; concurrent
// triggers that arrive while it is busy set a "rerun needed" flag and
// return immediately. After the in-flight pass finishes, the worker
// observes the flag and runs once more — coalescing any number of
// triggers received during the previous pass into a single follow-up.
//
// Modeled after the aiRegenInFlight pattern in ai_orchestrator.go. NOT
// `golang.org/x/sync/singleflight`: that collapses identical concurrent
// reads waiting for the same result, which is the wrong shape here. We
// don't have callers waiting on a return value — we have an idempotent
// background task that must run again whenever new data arrives, with
// at-most-one concurrency.
//
// `dirty` MUST be atomic: triggers write it concurrently with the
// worker's reads. A plain bool would trip the race detector and miss
// updates on weakly-ordered architectures.
type TenantRecompute struct {
	mu    sync.Mutex
	dirty atomic.Bool
}

// Trigger schedules `work` to run. If no worker is currently running,
// it spawns one and returns immediately; the worker keeps looping while
// new triggers arrive, with a debounce pause between passes. If a
// worker is already running, the dirty flag is set and Trigger returns
// without spawning anything — the running worker will re-run once it
// finishes its current pass.
//
// `ctx` controls worker lifecycle: when cancelled, the worker exits
// cleanly between passes (or during the debounce sleep) instead of
// blocking shutdown for up to one debounce window. A pending dirty flag
// at cancellation time is dropped — recompute is event-driven and the
// next ingest after restart will re-trigger naturally. The worker
// binds to the ctx of the Trigger that won TryLock; later Triggers'
// ctx values are not honoured. Callers MUST pass a stable per-tenant
// ctx (the same one used to start the tenant's other background
// routines).
//
// `work` must be safe to call concurrently across tenants but is
// guaranteed to be serialised within a single TenantRecompute. It must
// be idempotent: a recompute that observes the same source data twice
// must produce the same result.
func (t *TenantRecompute) Trigger(ctx context.Context, work func()) {
	if !t.mu.TryLock() {
		t.dirty.Store(true)
		return
	}
	go func() {
		for {
			if ctx.Err() != nil {
				t.mu.Unlock()
				return
			}
			t.dirty.Store(false)
			work()
			if t.dirty.Load() {
				select {
				case <-time.After(recomputeDebounce):
				case <-ctx.Done():
					t.mu.Unlock()
					return
				}
				continue
			}
			// Closing the unlock-race: between our dirty.Load() above
			// (false) and the Unlock below, a racing Trigger may
			// TryLock-fail and Store(dirty=true). That Trigger does NOT
			// spawn its own worker, so a naive `defer Unlock; return`
			// here would silently lose the recompute request. After
			// unlocking, re-read dirty: if true, attempt to reacquire
			// and run another pass. If reacquire fails, a Trigger that
			// arrived AFTER our Unlock has already spawned a fresh
			// worker — we can safely exit.
			if h := preUnlockHook.Load(); h != nil {
				(*h)()
			}
			t.mu.Unlock()
			if !t.dirty.Load() {
				return
			}
			if !t.mu.TryLock() {
				return
			}
		}
	}()
}
