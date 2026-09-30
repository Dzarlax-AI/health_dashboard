package storage

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRetryTodayDerivedStateError(t *testing.T) {
	if retryTodayDerivedStateError(ErrNoHourlyMetricData) {
		t.Fatal("empty tenant state must wait for ingestion, not retry")
	}
	if !retryTodayDerivedStateError(errors.New("temporary database failure")) {
		t.Fatal("transient failure must remain retryable")
	}
}

func testTodayCoordinator() *TodayDerivedStateCoordinator {
	c := NewTodayDerivedStateCoordinator(nil)
	c.refreshEnergy = func(context.Context, *DB, string, string) bool { return true }
	c.repeatDelay = time.Millisecond
	c.failureCooldown = 100 * time.Millisecond
	return c
}

func todayTimezone() string { return "UTC" }

func TestTodayDerivedRefreshMergesCacheInvalidationDuringWork(t *testing.T) {
	c := testTodayCoordinator()
	db := &DB{}
	deriveStarted := make(chan struct{})
	releaseDerive := make(chan struct{})
	secondDerive := make(chan []string, 1)
	var deriveCalls atomic.Int32
	var rebuildDates [][]string
	var mu sync.Mutex
	rebuild := func(dates []string) error {
		mu.Lock()
		rebuildDates = append(rebuildDates, append([]string(nil), dates...))
		mu.Unlock()
		return nil
	}
	derive := func(dates []string) error {
		if deriveCalls.Add(1) == 1 {
			close(deriveStarted)
			<-releaseDerive
			return nil
		}
		secondDerive <- append([]string(nil), dates...)
		return nil
	}

	c.TriggerRefresh(context.Background(), db, "health", TodayDerivedRefresh{Dates: []string{"2026-09-30"}, CacheReady: true}, rebuild, derive, todayTimezone, nil)
	<-deriveStarted
	// The new dirty signal for the in-flight date must survive its success.
	c.TriggerRefresh(context.Background(), db, "health", TodayDerivedRefresh{Dates: []string{"2026-09-30T12:00:00", "2026-10-01"}}, rebuild, derive, todayTimezone, nil)
	close(releaseDerive)

	select {
	case got := <-secondDerive:
		if want := []string{"2026-09-30", "2026-10-01"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("second derive dates = %v, want %v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("coalesced dependent refresh did not run")
	}
	mu.Lock()
	gotRebuild := append([][]string(nil), rebuildDates...)
	mu.Unlock()
	if want := [][]string{{"2026-09-30", "2026-10-01"}}; !reflect.DeepEqual(gotRebuild, want) {
		t.Fatalf("rebuild calls = %v, want %v", gotRebuild, want)
	}
}

func TestTodayDerivedRefreshMatchesLegacyOutputWithOneAggregation(t *testing.T) {
	type outcome struct {
		value      int
		aggregates int
		trace      []string
	}
	run := func(optimized bool) outcome {
		c := testTodayCoordinator()
		state := outcome{}
		rawValue := 7
		aggregate := func([]string) error {
			state.aggregates++
			state.trace = append(state.trace, "aggregate")
			state.value = rawValue
			return nil
		}
		ready := make(chan struct{})
		derive := func([]string) error {
			state.trace = append(state.trace, "derive")
			state.value *= 3
			return nil
		}
		c.refreshEnergy = func(context.Context, *DB, string, string) bool {
			state.trace = append(state.trace, "energy")
			return true
		}
		after := func() error {
			state.trace = append(state.trace, "today")
			close(ready)
			return nil
		}
		legacyRebuild := func(dates []string) error {
			if err := aggregate(dates); err != nil {
				return err
			}
			return derive(dates)
		}

		// Both paths have already performed the inline aggregate once.
		if err := aggregate([]string{"2026-09-30"}); err != nil {
			t.Fatal(err)
		}
		if optimized {
			c.TriggerRefresh(context.Background(), &DB{}, "health", TodayDerivedRefresh{Dates: []string{"2026-09-30"}, CacheReady: true}, aggregate, derive, todayTimezone, after)
		} else {
			c.Trigger(context.Background(), &DB{}, "health", []string{"2026-09-30"}, legacyRebuild, todayTimezone, after)
		}
		select {
		case <-ready:
		case <-time.After(time.Second):
			t.Fatal("synthetic refresh did not finish")
		}
		return state
	}

	legacy := run(false)
	optimized := run(true)
	if legacy.value != optimized.value {
		t.Fatalf("legacy output %d differs from optimized output %d", legacy.value, optimized.value)
	}
	if legacy.aggregates != 2 || optimized.aggregates != 1 {
		t.Fatalf("aggregation counts legacy=%d optimized=%d, want 2 and 1", legacy.aggregates, optimized.aggregates)
	}
	if want := []string{"aggregate", "aggregate", "derive", "energy", "today"}; !reflect.DeepEqual(legacy.trace, want) {
		t.Fatalf("legacy stage trace = %v, want %v", legacy.trace, want)
	}
	if want := []string{"aggregate", "derive", "energy", "today"}; !reflect.DeepEqual(optimized.trace, want) {
		t.Fatalf("optimized stage trace = %v, want %v", optimized.trace, want)
	}
}

func TestTodayDerivedRefreshRetryKeepsCompletedCacheAndEnforcesCooldown(t *testing.T) {
	c := testTodayCoordinator()
	db := &DB{}
	var rebuildCalls atomic.Int32
	var deriveCalls atomic.Int32
	failed := make(chan struct{})
	retried := make(chan struct{})
	rebuild := func([]string) error { rebuildCalls.Add(1); return nil }
	derive := func([]string) error {
		switch deriveCalls.Add(1) {
		case 1:
			close(failed)
			return errors.New("transient derive failure")
		default:
			close(retried)
			return nil
		}
	}

	started := time.Now()
	c.TriggerRefresh(context.Background(), db, "health", TodayDerivedRefresh{Dates: []string{"2026-09-30"}}, rebuild, derive, todayTimezone, nil)
	<-failed
	// A new event during the failure delay is coalesced and cannot bypass it.
	c.TriggerRefresh(context.Background(), db, "health", TodayDerivedRefresh{Dates: []string{"2026-10-01"}, CacheReady: true}, rebuild, derive, todayTimezone, nil)
	select {
	case <-retried:
		t.Fatal("failure cooldown was bypassed by a new trigger")
	case <-time.After(40 * time.Millisecond):
	}
	select {
	case <-retried:
	case <-time.After(time.Second):
		t.Fatal("failed derivation was not retried")
	}
	if elapsed := time.Since(started); elapsed < c.failureCooldown {
		t.Fatalf("retry started after %v, before cooldown %v", elapsed, c.failureCooldown)
	}
	if got := rebuildCalls.Load(); got != 1 {
		t.Fatalf("cache rebuild ran %d times; successful aggregation should be preserved", got)
	}
}

func TestTodayDerivedRefreshStageFailuresBlockDownstreamAndPreserveCache(t *testing.T) {
	t.Run("cache", func(t *testing.T) {
		c := testTodayCoordinator()
		c.failureCooldown = 10 * time.Millisecond
		var cacheCalls, deriveCalls, energyCalls, afterCalls atomic.Int32
		finished := make(chan struct{})
		c.refreshEnergy = func(context.Context, *DB, string, string) bool { energyCalls.Add(1); return true }
		c.TriggerRefresh(context.Background(), &DB{}, "health", TodayDerivedRefresh{Dates: []string{"2026-09-30"}}, func([]string) error {
			if cacheCalls.Add(1) == 1 {
				return errors.New("cache failure")
			}
			return nil
		}, func([]string) error { deriveCalls.Add(1); return nil }, todayTimezone, func() error {
			afterCalls.Add(1)
			close(finished)
			return nil
		})
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("cache retry did not complete")
		}
		if cacheCalls.Load() != 2 || deriveCalls.Load() != 1 || energyCalls.Load() != 1 || afterCalls.Load() != 1 {
			t.Fatalf("calls cache/derive/energy/after=%d/%d/%d/%d", cacheCalls.Load(), deriveCalls.Load(), energyCalls.Load(), afterCalls.Load())
		}
	})

	t.Run("energy", func(t *testing.T) {
		c := testTodayCoordinator()
		c.failureCooldown = 10 * time.Millisecond
		var deriveCalls, energyCalls, afterCalls atomic.Int32
		finished := make(chan struct{})
		c.refreshEnergy = func(context.Context, *DB, string, string) bool { return energyCalls.Add(1) > 1 }
		c.TriggerRefresh(context.Background(), &DB{}, "health", TodayDerivedRefresh{Dates: []string{"2026-09-30"}, CacheReady: true}, nil, func([]string) error {
			deriveCalls.Add(1)
			return nil
		}, todayTimezone, func() error { afterCalls.Add(1); close(finished); return nil })
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("energy retry did not complete")
		}
		if deriveCalls.Load() != 2 || energyCalls.Load() != 2 || afterCalls.Load() != 1 {
			t.Fatalf("calls derive/energy/after=%d/%d/%d", deriveCalls.Load(), energyCalls.Load(), afterCalls.Load())
		}
	})

	t.Run("after", func(t *testing.T) {
		c := testTodayCoordinator()
		c.failureCooldown = 10 * time.Millisecond
		var deriveCalls, energyCalls, afterCalls atomic.Int32
		finished := make(chan struct{})
		c.refreshEnergy = func(context.Context, *DB, string, string) bool { energyCalls.Add(1); return true }
		c.TriggerRefresh(context.Background(), &DB{}, "health", TodayDerivedRefresh{Dates: []string{"2026-09-30"}, CacheReady: true}, nil, func([]string) error {
			deriveCalls.Add(1)
			return nil
		}, todayTimezone, func() error {
			if afterCalls.Add(1) == 1 {
				return errors.New("after failure")
			}
			close(finished)
			return nil
		})
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("after retry did not complete")
		}
		if deriveCalls.Load() != 2 || energyCalls.Load() != 2 || afterCalls.Load() != 2 {
			t.Fatalf("calls derive/energy/after=%d/%d/%d", deriveCalls.Load(), energyCalls.Load(), afterCalls.Load())
		}
	})
}

func TestTodayDerivedRefreshWorkersAreIndependentPerDB(t *testing.T) {
	c := testTodayCoordinator()
	started := make(chan string, 2)
	release := make(chan struct{})
	derive := func(schema string) func([]string) error {
		return func([]string) error {
			started <- schema
			<-release
			return nil
		}
	}
	for _, schema := range []string{"tenant-a", "tenant-b"} {
		c.TriggerRefresh(context.Background(), &DB{}, schema, TodayDerivedRefresh{Dates: []string{"2026-09-30"}, CacheReady: true}, nil, derive(schema), todayTimezone, nil)
	}
	seen := map[string]bool{}
	for range 2 {
		select {
		case schema := <-started:
			seen[schema] = true
		case <-time.After(time.Second):
			close(release)
			t.Fatal("tenant workers serialized across independent DB pools")
		}
	}
	close(release)
	if !seen["tenant-a"] || !seen["tenant-b"] {
		t.Fatalf("started workers = %v", seen)
	}
}

func TestTodayDerivedRefreshCancellationPreservesPendingWork(t *testing.T) {
	c := testTodayCoordinator()
	db := &DB{}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	var derives atomic.Int32
	derive := func([]string) error {
		if derives.Add(1) == 1 {
			close(started)
			<-release
		}
		return nil
	}
	c.TriggerRefresh(ctx, db, "health", TodayDerivedRefresh{Dates: []string{"2026-09-30"}, CacheReady: true}, nil, derive, todayTimezone, nil)
	<-started
	c.TriggerRefresh(ctx, db, "health", TodayDerivedRefresh{Dates: []string{"2026-10-01"}, CacheReady: true}, nil, derive, todayTimezone, nil)
	cancel()
	close(release)

	tenant := c.tenantFor(db)
	waitFor(t, time.Second, func() bool {
		tenant.mu.Lock()
		defer tenant.mu.Unlock()
		return !tenant.running
	})
	if got := derives.Load(); got != 1 {
		t.Fatalf("cancelled worker ran %d derive passes, want 1", got)
	}
	completed := make(chan []string, 1)
	c.TriggerRefresh(context.Background(), db, "health", TodayDerivedRefresh{Dates: []string{"2026-10-02"}, CacheReady: true}, nil, func(dates []string) error {
		completed <- append([]string(nil), dates...)
		return nil
	}, todayTimezone, nil)
	select {
	case dates := <-completed:
		if want := []string{"2026-09-30", "2026-10-01", "2026-10-02"}; !reflect.DeepEqual(dates, want) {
			t.Fatalf("resumed dates = %v, want %v", dates, want)
		}
	case <-time.After(time.Second):
		t.Fatal("pending work was lost after cancellation")
	}
}
