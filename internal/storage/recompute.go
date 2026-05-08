package storage

import (
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
// `work` must be safe to call concurrently across tenants but is
// guaranteed to be serialised within a single TenantRecompute. It must
// be idempotent: a recompute that observes the same source data twice
// must produce the same result.
func (t *TenantRecompute) Trigger(work func()) {
	if !t.mu.TryLock() {
		t.dirty.Store(true)
		return
	}
	go func() {
		defer t.mu.Unlock()
		for {
			t.dirty.Store(false)
			work()
			if !t.dirty.Load() {
				return
			}
			time.Sleep(recomputeDebounce)
		}
	}()
}
