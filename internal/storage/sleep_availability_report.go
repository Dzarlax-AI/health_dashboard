package storage

import (
	"context"
	"fmt"
	"time"

	"health-receiver/internal/health"

	"github.com/jackc/pgx/v5"
)

// RecentSleepAvailabilityReport replays B0 from canonical rows only. It is a
// release-review read path: no raw payloads, snapshots, settings, providers,
// or writes are involved.
func (s *DB) RecentSleepAvailabilityReport(ctx context.Context, throughDate string, days int, asOf time.Time) (health.RecentSleepAvailabilityReport, error) {
	loc := s.reportTZLocation()
	through, err := time.ParseInLocation("2006-01-02", throughDate, loc)
	if err != nil {
		return health.RecentSleepAvailabilityReport{}, fmt.Errorf("parse report through date: %w", err)
	}
	if days < 1 || days > 366 {
		return health.RecentSleepAvailabilityReport{}, fmt.Errorf("availability report days must be 1..366")
	}
	// B0 needs its 90-day reference plus four current nights. The additional
	// week lets the cadence classifier explain a suppressed action without a
	// second database query for every evaluated day.
	from := through.AddDate(0, 0, -(days - 1 + 100)).Format("2006-01-02")
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return health.RecentSleepAvailabilityReport{}, fmt.Errorf("begin read-only availability report: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	records, err := listCompletedNightSleep(ctx, tx, from, throughDate)
	if err != nil {
		return health.RecentSleepAvailabilityReport{}, fmt.Errorf("list canonical nights: %w", err)
	}
	return health.BuildRecentSleepAvailabilityReport(records, throughDate, days, asOf, loc)
}
