package storage

import (
	"context"
	"encoding/json"
	"time"
)

// UpsertEnergySnapshot writes one EnergyBank v2 snapshot row keyed by
// the 5-minute ts_bucket. Multiple ingests landing in the same bucket
// upsert into the same row, last-write-wins; the caller is expected to
// have already debounced via TenantRecompute, so per-bucket churn is
// minimal.
//
// `tz` is used to compute the local-day `date` column; this is the
// same TZ that ComputeBankForToday consumed, so the date here matches
// the iteration's "today". Mismatched TZs across the read and write
// halves would produce snapshots whose date doesn't line up with the
// data they reflect.
func (s *DB) UpsertEnergySnapshot(ctx context.Context, tz string, res BankResult) error {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return err
	}
	tsBucket := time.Now().In(loc).Truncate(5 * time.Minute)
	dateStr := tsBucket.Format("2006-01-02")

	flags := res.Flags
	if flags == nil {
		flags = []string{}
	}

	var componentsJSON []byte
	if res.Components != nil {
		componentsJSON, err = json.Marshal(res.Components)
		if err != nil {
			return err
		}
	}

	_, err = s.pool.Exec(ctx, `
		INSERT INTO energy_snapshots (
			ts_bucket, date, bank, drain_delta, restore_delta,
			formula_version, components, flags, computed_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
		ON CONFLICT (ts_bucket) DO UPDATE SET
			date            = EXCLUDED.date,
			bank            = EXCLUDED.bank,
			drain_delta     = EXCLUDED.drain_delta,
			restore_delta   = EXCLUDED.restore_delta,
			formula_version = EXCLUDED.formula_version,
			components      = EXCLUDED.components,
			flags           = EXCLUDED.flags,
			computed_at     = NOW()`,
		tsBucket, dateStr, res.Bank, res.TodayDrain, res.TodayRestore,
		res.FormulaVersion, componentsJSON, flags)
	return err
}
