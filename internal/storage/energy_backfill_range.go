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
