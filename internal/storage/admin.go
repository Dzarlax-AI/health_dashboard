package storage

// TableStat holds row counts and date range for a pre-aggregated cache table.
type TableStat struct {
	Rows    int    `json:"rows"`
	Metrics int    `json:"metrics,omitempty"`
	Oldest  string `json:"oldest,omitempty"`
	Newest  string `json:"newest,omitempty"`
}

// CacheStatus is returned by GetCacheStatus and shown in the admin panel.
type CacheStatus struct {
	ScoreVersion    int       `json:"score_version"`
	LastSync        string    `json:"last_sync,omitempty"`
	RawPoints       TableStat `json:"raw_points"`
	MinuteCache     TableStat `json:"minute_cache"`
	HourlyCache     TableStat `json:"hourly_cache"`
	DailyScores     TableStat `json:"daily_scores"`
	TelegramEnabled bool      `json:"telegram_enabled"`
}

// GetCacheStatus returns row counts and date ranges for all cache tables.
func (s *DB) GetCacheStatus() (*CacheStatus, error) {
	cs := &CacheStatus{ScoreVersion: ScoreVersion}

	s.db.QueryRow(
		`SELECT COUNT(*), COUNT(DISTINCT metric_name) FROM metric_points WHERE qty > 0`,
	).Scan(&cs.RawPoints.Rows, &cs.RawPoints.Metrics)

	s.db.QueryRow(
		`SELECT COUNT(*), MIN(minute), MAX(minute) FROM minute_metrics`,
	).Scan(&cs.MinuteCache.Rows, &cs.MinuteCache.Oldest, &cs.MinuteCache.Newest)

	s.db.QueryRow(
		`SELECT COUNT(*), MIN(hour), MAX(hour) FROM hourly_metrics`,
	).Scan(&cs.HourlyCache.Rows, &cs.HourlyCache.Oldest, &cs.HourlyCache.Newest)

	s.db.QueryRow(
		`SELECT COUNT(*), MIN(date), MAX(date) FROM daily_scores`,
	).Scan(&cs.DailyScores.Rows, &cs.DailyScores.Oldest, &cs.DailyScores.Newest)

	s.db.QueryRow(
		`SELECT MAX(received_at) FROM health_records`,
	).Scan(&cs.LastSync)

	return cs, nil
}
