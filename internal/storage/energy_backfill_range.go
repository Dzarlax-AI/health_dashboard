package storage

import (
	"context"
)

// EarliestCompleteDailyScore returns the earliest date in daily_scores
// where every input the v2 formula consumes (sleep_total + sleep_deep +
// sleep_rem + sleep_awake + calories) is present. Used by
// cmd/energy_backfill to pick a sensible default lower bound — earlier
// dates would feed the formula nothing but imputation lookback and
// produce "stale" results anyway.
//
// Returns the empty string when daily_scores has zero such rows; the
// caller treats that as "no data to backfill" and exits.
// EnergyBackfillCoverage returns two counts used by the per-user
// settings page to render the backfill status line: how many days of
// daily_scores would be eligible for the v2 formula (all inputs
// present) and how many `energy_snapshots` rows already carry the
// `backfilled` flag.
//
// Two separate queries rather than one composite because the tables
// have nothing to join on cleanly (daily_scores keys on date string,
// energy_snapshots keys on ts_bucket); a CTE would be more code
// than two SELECTs and no clearer.
func (s *DB) EnergyBackfillCoverage(ctx context.Context) (completeDays int, backfilled int, err error) {
	if err = s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM daily_scores
		WHERE sleep_total IS NOT NULL
		  AND sleep_deep  IS NOT NULL
		  AND sleep_rem   IS NOT NULL
		  AND sleep_awake IS NOT NULL
		  AND calories    IS NOT NULL`).Scan(&completeDays); err != nil {
		return 0, 0, err
	}
	if err = s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM energy_snapshots
		WHERE 'backfilled' = ANY(flags)`).Scan(&backfilled); err != nil {
		return 0, 0, err
	}
	return completeDays, backfilled, nil
}

func (s *DB) EarliestCompleteDailyScore(ctx context.Context) (string, error) {
	var date *string
	err := s.pool.QueryRow(ctx, `
		SELECT MIN(date)
		FROM daily_scores
		WHERE sleep_total IS NOT NULL
		  AND sleep_deep IS NOT NULL
		  AND sleep_rem IS NOT NULL
		  AND sleep_awake IS NOT NULL
		  AND calories IS NOT NULL`).Scan(&date)
	if err != nil {
		return "", err
	}
	if date == nil {
		return "", nil
	}
	return *date, nil
}
