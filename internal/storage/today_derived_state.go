package storage

import (
	"context"
	"errors"
	"log"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

const (
	todayDerivedDebounce     = 2 * time.Second
	todayDerivedFailureDelay = 60 * time.Second
)

func retryTodayDerivedStateError(err error) bool {
	return err != nil && !errors.Is(err, ErrNoHourlyMetricData)
}

// TodayDerivedRefresh describes which source dates have changed and whether
// their aggregate cache has already been refreshed by the caller.
type TodayDerivedRefresh struct {
	Dates      []string
	CacheReady bool
}

// TodayDerivedStateCoordinator owns the causal order for values shown on
// Today. It coalesces tenant changes while keeping aggregate-cache work
// separate from downstream derived-state work.
type TodayDerivedStateCoordinator struct {
	mu      sync.Mutex
	energy  *EnergyV2Orchestrator
	tenants map[*DB]*todayDerivedTenant
	nextID  atomic.Uint64

	// These seams keep coordinator scheduling deterministic in unit tests.
	refreshEnergy   func(context.Context, *DB, string, string) bool
	repeatDelay     time.Duration
	failureCooldown time.Duration
}

type todayDerivedStateBatch struct {
	dates map[string]bool // true means aggregation is still required
	any   bool
}

type todayDerivedTenant struct {
	mu      sync.Mutex
	id      uint64
	running bool
	ctx     context.Context
	db      *DB

	pendingDates map[string]bool
	pendingAny   bool

	rebuild  func([]string) error
	derive   func([]string) error
	timezone func() string
	after    func() error
	schema   string
}

// NewTodayDerivedStateCoordinator creates a process-wide coordinator. It is
// safe to share across all tenants; each DB pool receives its own worker.
func NewTodayDerivedStateCoordinator(energy *EnergyV2Orchestrator) *TodayDerivedStateCoordinator {
	return &TodayDerivedStateCoordinator{
		energy:          energy,
		tenants:         make(map[*DB]*todayDerivedTenant),
		repeatDelay:     todayDerivedDebounce,
		failureCooldown: todayDerivedFailureDelay,
	}
}

// Trigger preserves the original API. Its dates are considered cache-dirty,
// and it does not run a separate readiness derivation callback.
func (c *TodayDerivedStateCoordinator) Trigger(
	ctx context.Context,
	db *DB,
	schema string,
	dates []string,
	rebuild func([]string) error,
	timezone func() string,
	after func() error,
) {
	c.TriggerRefresh(ctx, db, schema, TodayDerivedRefresh{Dates: dates}, rebuild, nil, timezone, after)
}

// TriggerRefresh is non-blocking. CacheReady indicates that aggregate cache
// work has already completed for these dates. A cache-dirty request wins when
// events for the same date are coalesced. Empty-date refreshes still execute
// the dependent stages, for example after a subjective check-in.
func (c *TodayDerivedStateCoordinator) TriggerRefresh(
	ctx context.Context,
	db *DB,
	schema string,
	request TodayDerivedRefresh,
	rebuild func([]string) error,
	derive func([]string) error,
	timezone func() string,
	after func() error,
) {
	if c == nil || db == nil || timezone == nil || (c.energy == nil && c.refreshEnergy == nil) {
		return
	}
	tenant := c.tenantFor(db)

	tenant.mu.Lock()
	for _, rawDate := range request.Dates {
		if len(rawDate) < 10 {
			continue
		}
		date := rawDate[:10]
		cacheDirty := !request.CacheReady
		if oldDirty, exists := tenant.pendingDates[date]; !exists || cacheDirty {
			tenant.pendingDates[date] = cacheDirty || oldDirty
		}
		tenant.pendingAny = true
	}
	if len(request.Dates) == 0 {
		tenant.pendingAny = true
	}
	tenant.rebuild = rebuild
	tenant.derive = derive
	tenant.timezone = timezone
	tenant.after = after
	tenant.schema = schema
	if !tenant.running {
		tenant.running = true
		tenant.ctx = ctx
		tenant.db = db
		go c.run(ctx, db, tenant)
	}
	tenant.mu.Unlock()
}

func (c *TodayDerivedStateCoordinator) tenantFor(db *DB) *todayDerivedTenant {
	c.mu.Lock()
	defer c.mu.Unlock()
	tenant := c.tenants[db]
	if tenant == nil {
		tenant = &todayDerivedTenant{
			id:           c.nextID.Add(1),
			pendingDates: make(map[string]bool),
		}
		c.tenants[db] = tenant
	}
	return tenant
}

func (c *TodayDerivedStateCoordinator) run(ctx context.Context, db *DB, tenant *todayDerivedTenant) {
	first := true
	var pass uint64
	for {
		if ctx.Err() != nil {
			c.finishWorker(tenant)
			return
		}
		if !first {
			if !c.wait(ctx, c.repeatDelay) {
				c.finishWorker(tenant)
				return
			}
		}
		first = false

		batch, rebuild, derive, timezone, after, schema, ok := takeTodayDerivedBatch(tenant)
		if !ok {
			c.finishWorker(tenant)
			return
		}
		pass++
		passStarted := time.Now()
		captured, err := captureTodayCacheDirty(ctx, db, batch.dates)
		if err != nil {
			c.requeue(tenant, batch, false)
			logTodayDerivedStage(tenant.id, pass, "journal", passStarted, len(batch.dates), err)
			if !c.wait(ctx, c.failureCooldown) {
				c.finishWorker(tenant)
				return
			}
			first = true
			continue
		}
		cacheDates := cacheDirtyDates(batch.dates)
		cacheComplete := len(cacheDates) == 0
		if !cacheComplete && rebuild != nil {
			started := time.Now()
			err := rebuild(cacheDates)
			logTodayDerivedStage(tenant.id, pass, "cache", started, len(cacheDates), err)
			if err != nil {
				if retryTodayDerivedStateError(err) {
					c.requeue(tenant, batch, false)
					logTodayDerivedPass(tenant.id, pass, passStarted, "failed")
					if !c.wait(ctx, c.failureCooldown) {
						c.finishWorker(tenant)
						return
					}
					first = true
				} else {
					log.Printf("today derived state tenant=%d pass=%d stage=cache result=empty retry=false", tenant.id, pass)
					if !c.repeatIfPending(tenant) {
						logTodayDerivedPass(tenant.id, pass, passStarted, "empty")
						return
					}
				}
				if retryTodayDerivedStateError(err) {
					continue
				}
				first = false
				continue
			}
			cacheComplete = true
		}
		if !cacheComplete {
			// Without a rebuild callback, preserve the established optional
			// callback behavior and proceed to dependent work.
			cacheComplete = true
		}

		allDates := sortedTodayDerivedDates(batch.dates)
		if ctx.Err() != nil {
			c.requeue(tenant, batch, true)
			c.finishWorker(tenant)
			return
		}
		if derive != nil {
			started := time.Now()
			err := derive(allDates)
			logTodayDerivedStage(tenant.id, pass, "derive", started, len(allDates), err)
			if err != nil {
				if retryTodayDerivedStateError(err) {
					c.requeue(tenant, batch, true)
					logTodayDerivedPass(tenant.id, pass, passStarted, "failed")
					if !c.wait(ctx, c.failureCooldown) {
						c.finishWorker(tenant)
						return
					}
					first = true
				} else {
					log.Printf("today derived state tenant=%d pass=%d stage=derive result=empty retry=false", tenant.id, pass)
					if !c.repeatIfPending(tenant) {
						logTodayDerivedPass(tenant.id, pass, passStarted, "empty")
						return
					}
				}
				if retryTodayDerivedStateError(err) {
					continue
				}
				first = false
				continue
			}
		}
		if ctx.Err() != nil {
			c.requeue(tenant, batch, true)
			c.finishWorker(tenant)
			return
		}

		tenant.mu.Lock()
		tzNow, schemaNow, afterNow := tenant.timezone, tenant.schema, tenant.after
		tenant.mu.Unlock()
		if tzNow == nil {
			tzNow = timezone
		}
		if schemaNow == "" {
			schemaNow = schema
		}
		started := time.Now()
		ok = c.refresh(ctx, db, schemaNow, tzNow())
		logTodayDerivedStageResult(tenant.id, pass, "energy", started, len(allDates), ok)
		if !ok {
			c.requeue(tenant, batch, true)
			logTodayDerivedPass(tenant.id, pass, passStarted, "failed")
			if !c.wait(ctx, c.failureCooldown) {
				c.finishWorker(tenant)
				return
			}
			first = true
			continue
		}
		if afterNow == nil {
			afterNow = after
		}
		if afterNow != nil {
			started = time.Now()
			err := afterNow()
			logTodayDerivedStage(tenant.id, pass, "after", started, len(allDates), err)
			if err != nil {
				c.requeue(tenant, batch, true)
				logTodayDerivedPass(tenant.id, pass, passStarted, "failed")
				if !c.wait(ctx, c.failureCooldown) {
					c.finishWorker(tenant)
					return
				}
				first = true
				continue
			}
		}
		if db.pool != nil {
			db.cacheMu.Lock()
			err = db.CompleteCacheDirty(ctx, captured)
			db.cacheMu.Unlock()
			if err != nil {
				c.requeue(tenant, batch, true)
				logTodayDerivedStage(tenant.id, pass, "journal_complete", passStarted, len(captured), err)
				if !c.wait(ctx, c.failureCooldown) {
					c.finishWorker(tenant)
					return
				}
				first = true
				continue
			}
		}
		logTodayDerivedPass(tenant.id, pass, passStarted, "ok")
		if !c.repeatIfPending(tenant) {
			return
		}
		first = false
	}
}

// Capture all durable dirty dates, including mutations whose in-memory signal
// was lost on a restart. CacheReady is valid only for the exact generation
// successfully aggregated by this process, never for a newer correction.
func captureTodayCacheDirty(ctx context.Context, db *DB, dates map[string]bool) (map[string]uint64, error) {
	if db.pool == nil { // Coordinator unit tests inject all I/O callbacks.
		return nil, nil
	}
	db.cacheMu.Lock()
	defer db.cacheMu.Unlock()
	captured, err := db.CaptureCacheDirty(ctx, nil)
	if err != nil {
		return nil, err
	}
	for date, generation := range captured {
		dates[date] = dates[date] || db.cacheAppliedGenerations[date] != generation
	}
	return captured, nil
}

// WaitForIdle observes completion of the tenant's queued dependent stages.
// It does not report success while failures are being retried or work remains.
func (c *TodayDerivedStateCoordinator) WaitForIdle(ctx context.Context, db *DB) error {
	if c == nil {
		return errors.New("today coordinator unavailable")
	}
	tenant := c.tenantFor(db)
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		tenant.mu.Lock()
		idle := !tenant.running && !tenant.pendingAny
		tenant.mu.Unlock()
		if idle {
			return ctx.Err()
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func takeTodayDerivedBatch(tenant *todayDerivedTenant) (todayDerivedStateBatch, func([]string) error, func([]string) error, func() string, func() error, string, bool) {
	tenant.mu.Lock()
	defer tenant.mu.Unlock()
	if !tenant.pendingAny {
		return todayDerivedStateBatch{}, nil, nil, nil, nil, "", false
	}
	batch := todayDerivedStateBatch{dates: tenant.pendingDates, any: tenant.pendingAny}
	tenant.pendingDates = make(map[string]bool)
	tenant.pendingAny = false
	return batch, tenant.rebuild, tenant.derive, tenant.timezone, tenant.after, tenant.schema, true
}

// cacheComplete indicates whether aggregation succeeded for this batch. If
// it did not, retain each date's original cache-dirty state for retry.
func (c *TodayDerivedStateCoordinator) requeue(tenant *todayDerivedTenant, batch todayDerivedStateBatch, cacheComplete bool) {
	tenant.mu.Lock()
	defer tenant.mu.Unlock()
	tenant.pendingAny = tenant.pendingAny || batch.any
	for date, dirty := range batch.dates {
		if cacheComplete {
			dirty = false
		}
		if existing, ok := tenant.pendingDates[date]; !ok || dirty {
			tenant.pendingDates[date] = dirty || existing
		}
	}
}

func (c *TodayDerivedStateCoordinator) finishWorker(tenant *todayDerivedTenant) {
	tenant.mu.Lock()
	tenant.running = false
	// A trigger can arrive as the worker is finishing. Restart while still
	// holding the tenant mutex so that no pending event can be stranded.
	if tenant.pendingAny && tenant.ctx != nil && tenant.ctx.Err() == nil {
		tenant.running = true
		go c.run(tenant.ctx, tenant.db, tenant)
	}
	tenant.mu.Unlock()
}

// repeatIfPending atomically chooses between a debounced follow-up and an
// idle worker. A trigger arriving after idle is set starts a fresh immediate
// pass; a trigger already queued during work gets the repeat delay.
func (c *TodayDerivedStateCoordinator) repeatIfPending(tenant *todayDerivedTenant) bool {
	tenant.mu.Lock()
	defer tenant.mu.Unlock()
	if tenant.pendingAny {
		return true
	}
	tenant.running = false
	return false
}

func (c *TodayDerivedStateCoordinator) wait(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return ctx.Err() == nil
	case <-ctx.Done():
		return false
	}
}

func (c *TodayDerivedStateCoordinator) refresh(ctx context.Context, db *DB, schema, timezone string) bool {
	if c.refreshEnergy != nil {
		return c.refreshEnergy(ctx, db, schema, timezone)
	}
	return c.energy != nil && c.energy.Refresh(ctx, db, schema, timezone)
}

func cacheDirtyDates(dates map[string]bool) []string {
	all := sortedTodayDerivedDates(dates)
	out := make([]string, 0, len(all))
	for _, date := range all {
		if dates[date] {
			out = append(out, date)
		}
	}
	return out
}

func sortedTodayDerivedDates(dates map[string]bool) []string {
	out := make([]string, 0, len(dates))
	for date := range dates {
		out = append(out, date)
	}
	sort.Strings(out)
	return out
}

func logTodayDerivedStage(tenantID uint64, pass uint64, stage string, started time.Time, dateCount int, err error) {
	result := "ok"
	if err != nil {
		result = "failed"
	}
	log.Printf("today derived state tenant=%d pass=%d stage=%s result=%s dates=%d duration=%s", tenantID, pass, stage, result, dateCount, time.Since(started).Round(time.Millisecond))
}

func logTodayDerivedStageResult(tenantID uint64, pass uint64, stage string, started time.Time, dateCount int, ok bool) {
	result := "ok"
	if !ok {
		result = "failed"
	}
	log.Printf("today derived state tenant=%d pass=%d stage=%s result=%s dates=%d duration=%s", tenantID, pass, stage, result, dateCount, time.Since(started).Round(time.Millisecond))
}

func logTodayDerivedPass(tenantID uint64, pass uint64, started time.Time, result string) {
	log.Printf("today derived state tenant=%d pass=%d result=%s duration=%s", tenantID, pass, result, time.Since(started).Round(time.Millisecond))
}
