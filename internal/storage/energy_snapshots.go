package storage

import (
	"context"
	"log"
	"time"
)

// EnsureEnergySnapshotsTable creates the energy_snapshots table used by
// EnergyBank v2. Idempotent — safe to call on every startup.
//
// The `date` column is intentionally NOT a Postgres GENERATED column.
// GENERATED requires an IMMUTABLE expression, which forces a hardcoded
// timezone in DDL — wrong for a multi-tenant system where each tenant's
// REPORT_TZ may differ. The writer computes `date` in Go using the
// tenant's timezone before INSERT.
//
// `ts_bucket` (TIMESTAMPTZ) is the canonical absolute time for cross-TZ
// comparison; `date` (TEXT) exists for fast day-bucketed queries.
func (s *DB) EnsureEnergySnapshotsTable() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS energy_snapshots (
			ts_bucket       TIMESTAMPTZ NOT NULL,
			date            TEXT NOT NULL,
			bank            INTEGER NOT NULL,
			drain_delta     INTEGER NOT NULL,
			restore_delta   INTEGER NOT NULL,
			formula_version INTEGER NOT NULL,
			components      JSONB,
			flags           TEXT[] NOT NULL DEFAULT '{}',
			computed_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			PRIMARY KEY (ts_bucket)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_energy_snapshots_date ON energy_snapshots (date DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_energy_snapshots_ts ON energy_snapshots (ts_bucket DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_energy_snapshots_flags ON energy_snapshots USING GIN (flags)`,
	}
	for _, q := range stmts {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			log.Printf("EnsureEnergySnapshotsTable: %v", err)
		}
	}
}
