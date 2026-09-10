package storage

import (
	"context"
	"errors"
	"log"
	"sync"
	"time"
)

const todayDerivedDebounce = 60 * time.Second

func retryTodayDerivedStateError(err error) bool {
	return err != nil && !errors.Is(err, ErrNoHourlyMetricData)
}

// TodayDerivedStateCoordinator owns the causal order for values shown on
// Today.  Ingest and other mutations only mark a tenant dirty; one coalesced
// worker then rebuilds the affected cache dates, writes the canonical Energy
// snapshot, and finally lets callers invalidate dependent presentation state.
//
// This deliberately sits above EnergyV2Orchestrator.  TriggerAfter alone is
// only an Energy barrier and may otherwise race the date-aware cache rebuild.
type TodayDerivedStateCoordinator struct {
	mu      sync.Mutex
	energy  *EnergyV2Orchestrator
	tenants map[*DB]*todayDerivedTenant
}

type todayDerivedTenant struct {
	recompute TenantRecompute
	mu        sync.Mutex
	dates     map[string]struct{}
	rebuild   func([]string) error
	timezone  func() string
	after     func() error
	schema    string
}

// NewTodayDerivedStateCoordinator creates a process-wide coordinator.  It is
// safe to share across all tenants; each DB pool receives its own worker.
func NewTodayDerivedStateCoordinator(energy *EnergyV2Orchestrator) *TodayDerivedStateCoordinator {
	return &TodayDerivedStateCoordinator{
		energy:  energy,
		tenants: make(map[*DB]*todayDerivedTenant),
	}
}

// Trigger is non-blocking. dates may be empty for mutations such as a
// subjective check-in that alter guidance but do not need cache rebuilds.
// The latest callbacks are retained so settings changes take effect before a
// queued rerun starts.
func (c *TodayDerivedStateCoordinator) Trigger(
	ctx context.Context,
	db *DB,
	schema string,
	dates []string,
	rebuild func([]string) error,
	timezone func() string,
	after func() error,
) {
	if c == nil || c.energy == nil || db == nil || timezone == nil {
		return
	}
	c.mu.Lock()
	tenant := c.tenants[db]
	if tenant == nil {
		tenant = &todayDerivedTenant{dates: make(map[string]struct{}), recompute: *NewTenantRecompute(todayDerivedDebounce)}
		c.tenants[db] = tenant
	}
	c.mu.Unlock()

	tenant.mu.Lock()
	for _, date := range dates {
		if len(date) >= 10 {
			tenant.dates[date[:10]] = struct{}{}
		}
	}
	tenant.rebuild = rebuild
	tenant.timezone = timezone
	tenant.after = after
	tenant.schema = schema
	tenant.mu.Unlock()

	tenant.recompute.Trigger(ctx, func() {
		tenant.mu.Lock()
		dates := make([]string, 0, len(tenant.dates))
		for date := range tenant.dates {
			dates = append(dates, date)
		}
		tenant.dates = make(map[string]struct{})
		rebuildNow, timezoneNow, afterNow, schemaNow := tenant.rebuild, tenant.timezone, tenant.after, tenant.schema
		tenant.mu.Unlock()
		retry := func() {
			tenant.mu.Lock()
			for _, date := range dates {
				tenant.dates[date] = struct{}{}
			}
			tenant.mu.Unlock()
			// The current worker holds TenantRecompute.mu, so this only sets
			// its dirty bit. The worker will retry after the configured debounce.
			tenant.recompute.Trigger(ctx, func() {})
		}

		if rebuildNow != nil && len(dates) > 0 {
			if err := rebuildNow(dates); err != nil {
				if !retryTodayDerivedStateError(err) {
					log.Printf("[%s] today derived state: awaiting first hourly metric", schemaNow)
					return
				}
				log.Printf("[%s] today derived state: cache rebuild failed: %v", schemaNow, err)
				retry()
				return
			}
		}
		if !c.energy.Refresh(ctx, db, schemaNow, timezoneNow()) {
			retry()
			return
		}
		if afterNow != nil {
			if err := afterNow(); err != nil {
				log.Printf("[%s] today derived state: dependent refresh failed: %v", schemaNow, err)
				retry()
			}
		}
	})
}
