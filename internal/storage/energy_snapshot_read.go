package storage

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// LatestEnergySnapshot is a read-only projection of the freshest
// energy_snapshots row for a given local date — the value that should
// drive "what's my bank right now?" rendering. Distinct from
// BankResult (compute side) so callers can't accidentally feed a
// half-populated read object back into the writer.
type LatestEnergySnapshot struct {
	Bank           int
	DrainDelta     int
	RestoreDelta   int
	FormulaVersion int
	Flags          []string
}

// GetLatestEnergySnapshotForDate returns the most recent (by
// ts_bucket) energy_snapshots row whose `date` column matches
// `dateStr` ("YYYY-MM-DD" in the tenant's TZ). Returns nil with no
// error when no row exists for that date — a fresh tenant or a
// recompute that hasn't run yet for today should not surface as a
// hard error to the briefing path; the caller must fall through to
// the legacy EnergyBank instead.
func (s *DB) GetLatestEnergySnapshotForDate(ctx context.Context, dateStr string) (*LatestEnergySnapshot, error) {
	// Restrict the sort to this day. An unbounded timestamp-index walk can scan
	// years of newer snapshots before reaching a historical date.
	row := s.pool.QueryRow(ctx, `
		WITH day AS MATERIALIZED (
			SELECT ts_bucket, bank, drain_delta, restore_delta, formula_version, flags
			FROM `+s.productArchiveRelation(ctx, "energy_snapshots", "tenant_native_energy_snapshot_v1")+`
			WHERE date = $1
		)
		SELECT bank, drain_delta, restore_delta, formula_version, flags FROM day
		ORDER BY ts_bucket DESC LIMIT 1`, dateStr)

	var snap LatestEnergySnapshot
	if err := row.Scan(&snap.Bank, &snap.DrainDelta, &snap.RestoreDelta,
		&snap.FormulaVersion, &snap.Flags); err != nil {
		// Surface "no row for that date" as nil/nil so callers handle
		// fresh-tenant / not-yet-computed cases without distinguishing
		// it from a real DB error.
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if snap.Flags == nil {
		snap.Flags = []string{}
	}
	return &snap, nil
}
