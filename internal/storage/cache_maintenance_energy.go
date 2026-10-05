package storage

import (
	"context"
	"encoding/json"
	"time"
)

// Historical corrections replace synthetic EOD estimates only. A captured
// intraday observation remains an audit of inputs available at that time.
func (s *DB) repairHistoricalEnergyDate(ctx context.Context, date, tz string) error {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return err
	}
	if date >= time.Now().In(loc).Format(isoDate) {
		return nil
	}
	res, err := s.ComputeBankForDate(ctx, tz, date)
	if err != nil {
		return err
	}
	ts, err := time.ParseInLocation("2006-01-02 15:04", date+" 23:55", loc)
	if err != nil {
		return err
	}
	ts = ts.Truncate(5 * time.Minute)
	if res.State == "stale" {
		_, err = s.pool.Exec(ctx, `DELETE FROM energy_snapshots WHERE ts_bucket=$1 AND 'backfilled'=ANY(flags)`, ts)
		return err
	}
	res.Flags = append(res.Flags, "backfilled")
	raw, err := json.Marshal(res.Components)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO energy_snapshots
		(ts_bucket,date,bank,drain_delta,restore_delta,formula_version,components,flags,computed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,NOW())
		ON CONFLICT(ts_bucket) DO UPDATE SET bank=EXCLUDED.bank,
		drain_delta=EXCLUDED.drain_delta,restore_delta=EXCLUDED.restore_delta,
		formula_version=EXCLUDED.formula_version,components=EXCLUDED.components,
		flags=EXCLUDED.flags,computed_at=NOW()
		WHERE 'backfilled'=ANY(energy_snapshots.flags)`,
		ts, date, res.Bank, res.TodayDrain, res.TodayRestore,
		res.FormulaVersion, json.RawMessage(raw), res.Flags)
	return err
}
