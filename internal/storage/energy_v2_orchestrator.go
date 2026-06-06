package storage

import (
	"context"
	"log"
	"sync"
)

// EnergyV2Orchestrator is the production glue between the read-side
// computation (ComputeBankForToday, PR4) and the write-side persistence
// (UpsertEnergySnapshot, this PR). One TenantRecompute per DB pool
// serialises recomputes for that tenant and coalesces ingest bursts
// into one immediate compute + at most one follow-up.
//
// The dashboard and reports now consume the latest v2 energy_snapshots row
// when present. The legacy daily EnergyBank remains only as the briefing
// fallback for fresh tenants or days before the first v2 snapshot lands.
//
// The orchestrator owns no resources beyond the per-tenant
// TenantRecompute structs; the lifecycle ctx for those workers is
// supplied at trigger time and is expected to be the same stable
// per-tenant ctx used elsewhere (otherwise the worker would be bound
// to a request-scoped ctx and exit prematurely — see the docstring on
// TenantRecompute.Trigger).
type EnergyV2Orchestrator struct {
	mu  sync.Mutex
	rcs map[*DB]*TenantRecompute
}

// NewEnergyV2Orchestrator returns a process-wide orchestrator. Single
// instance per binary, shared across all tenants.
func NewEnergyV2Orchestrator() *EnergyV2Orchestrator {
	return &EnergyV2Orchestrator{rcs: make(map[*DB]*TenantRecompute)}
}

// Trigger schedules a recompute for the given tenant. Returns
// immediately whether or not a worker was already running. `tz` is
// the tenant's REPORT_TZ — read fresh on every trigger because the
// user may have edited it in /settings since the worker started.
//
// `schema` is used only for log lines; it has no functional role.
func (o *EnergyV2Orchestrator) Trigger(ctx context.Context, db *DB, schema, tz string) {
	o.mu.Lock()
	rc, ok := o.rcs[db]
	if !ok {
		rc = &TenantRecompute{}
		o.rcs[db] = rc
	}
	o.mu.Unlock()

	rc.Trigger(ctx, func() {
		o.recompute(ctx, db, schema, tz)
	})
}

func (o *EnergyV2Orchestrator) recompute(ctx context.Context, db *DB, schema, tz string) {
	res, err := db.ComputeBankForToday(ctx, tz)
	if err != nil {
		log.Printf("[ENERGY_V2] schema=%s compute error: %v", schema, err)
		return
	}
	if err := db.UpsertEnergySnapshot(ctx, tz, res); err != nil {
		log.Printf("[ENERGY_V2] schema=%s upsert error: %v", schema, err)
		return
	}
	// One log line per recompute lets us observe v2 behaviour in production
	// without depending on request-path rendering.
	log.Printf("[ENERGY_V2] schema=%s bank=%d display=%d state=%s flags=%v alpha=%.4f drain=%d restore=%d",
		schema, res.Bank, res.Display, res.State, res.Flags, res.AlphaUsed, res.TodayDrain, res.TodayRestore)
}
