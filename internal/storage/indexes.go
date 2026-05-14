package storage

import "log"

// EnsureIndexes creates expression indexes that speed up common queries and
// applies schema migrations that aren't part of init.sql. Safe to call on
// every startup — uses IF NOT EXISTS.
func (s *DB) EnsureIndexes() {
	ctx, cancel := longCtx()
	defer cancel()

	// Schema migrations. Kept here (not in init.sql) so existing deployments
	// pick them up without manual intervention. ADD COLUMN IF NOT EXISTS is a
	// metadata-only change in Postgres ≥ 11 — fast even on the 3.7M-row table.
	migrations := []string{
		// quality flag for soft-suspect / hard-impossible filtering. Default
		// 'ok' so existing rows behave identically until something flips them.
		`ALTER TABLE metric_points ADD COLUMN IF NOT EXISTS quality TEXT NOT NULL DEFAULT 'ok'`,
		// EnergyBank EOD snapshot columns. Capacity / current / drain are 0–100;
		// verdict is "rest" / "active_recovery" / "moderate" / "push_hard" (matches
		// EnergyBank.ActionVerdict). NULL means no snapshot was taken — the row
		// pre-dates the persistence wiring or briefing didn't run that day.
		`ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS energy_capacity INTEGER`,
		`ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS energy_eod_current INTEGER`,
		`ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS energy_drain INTEGER`,
		`ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS energy_verdict TEXT`,

		// v2.2 stress methodology — median HR over the last 3h of the
		// main sleep segment ending on this date. NOT to be confused
		// with `rhr_avg` (which is a per-day average of heart_rate —
		// see STRESS_MEASUREMENT.md §0 blocker 1). NULL until the
		// `upsertBaselineHROvernightForDate` writer runs for the date.
		`ALTER TABLE daily_scores ADD COLUMN IF NOT EXISTS baseline_hr_overnight REAL`,
	}
	for _, ddl := range migrations {
		if _, err := s.pool.Exec(ctx, ddl); err != nil {
			log.Printf("migrate: %v (query: %.80s)", err, ddl)
		}
	}

	indexes := []string{
		// Partial index covers the hot path — baseline reads filter quality='ok'.
		// Defined inline (not in the migration block) because it depends on the
		// quality column existing first.
		`CREATE INDEX IF NOT EXISTS idx_points_quality_metric ON metric_points (metric_name, SUBSTRING(date,1,10)) WHERE quality = 'ok'`,

		// Speeds up GROUP BY / ORDER BY on the date part of hourly_metrics.hour
		`CREATE INDEX IF NOT EXISTS idx_hourly_date ON hourly_metrics (SUBSTRING(hour,1,10))`,

		// Speeds up WHERE metric_name = $1 AND date-part queries on hourly_metrics
		`CREATE INDEX IF NOT EXISTS idx_hourly_metric_date ON hourly_metrics (metric_name, SUBSTRING(hour,1,10))`,

		// Speeds up GROUP BY / ORDER BY on the date part of metric_points.date
		`CREATE INDEX IF NOT EXISTS idx_points_date ON metric_points (SUBSTRING(date,1,10))`,

		// Speeds up WHERE metric_name = $1 AND date-part queries on metric_points
		`CREATE INDEX IF NOT EXISTS idx_points_metric_date ON metric_points (metric_name, SUBSTRING(date,1,10))`,
	}

	for _, ddl := range indexes {
		if _, err := s.pool.Exec(ctx, ddl); err != nil {
			log.Printf("ensure index: %v (query: %.80s)", err, ddl)
		}
	}
}
