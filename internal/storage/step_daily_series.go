package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// DailyStepPoint carries the published daily quantity plotted by the web UI.
type DailyStepPoint struct {
	Date string  `json:"date"`
	Qty  float64 `json:"qty"`
}

const publishedStepSeries = `select date, steps::float8 as qty from daily_scores
	where date between $1 and $2 and steps is not null`

// DailyStepSeries uses a single database snapshot so a concurrent source edit
// cannot land between checking its dirty generation and reading the rollup.
func (s *DB) DailyStepSeries(ctx context.Context, from, to string) ([]DailyStepPoint, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var points []DailyStepPoint
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var state CacheMaintenanceState
		var raw string
		err := tx.QueryRow(ctx, "select value from settings where key=$1", cacheMaintenanceSettingKey).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// Missing or unrecognizable journal metadata cannot certify the rollup.
		if err == nil {
			state, _ = decodeCacheMaintenanceState(raw)
		}
		args := []any{from, to}
		query := publishedStepSeries
		if s.profileGraphSchema != "" || state.TargetIdentity != "" || state.CompletedIdentity != CacheMaintenanceIdentity() {
			query = "select day as date, source_total::real::float8 as qty from (" +
				physicalStepTotalsSQL("substring(p.date,1,10) between $1 and $2", "hourly_metrics", "substring(hour,1,10) between $1 and $2") + ") recomputed"
		} else {
			var dirty []string
			for day := range state.Dirty {
				if from <= day && day <= to {
					dirty = append(dirty, day)
				}
			}
			if len(dirty) > 0 {
				args = append(args, dirty)
				query += " and date <> all($3::text[]) union all select day as date, source_total::real::float8 as qty from (" +
					physicalStepTotalsSQL("substring(p.date,1,10)=any($3::text[])", "hourly_metrics", "substring(hour,1,10)=any($3::text[])") + ") recomputed"
			}
		}
		rows, err := tx.Query(ctx, query+" order by date", args...)
		if err != nil {
			return err
		}
		points, err = pgx.CollectRows(rows, pgx.RowToStructByPos[DailyStepPoint])
		return err
	})
	return points, err
}
