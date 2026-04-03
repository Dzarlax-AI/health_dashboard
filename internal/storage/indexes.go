package storage

import "log"

// EnsureIndexes creates expression indexes that speed up common queries.
// Safe to call on every startup — uses IF NOT EXISTS.
func (s *DB) EnsureIndexes() {
	ctx, cancel := longCtx()
	defer cancel()

	indexes := []string{
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
