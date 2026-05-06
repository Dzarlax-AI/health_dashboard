package storage

import "time"

// WeeklyQualityStats summarises data-quality findings over a window. Used by
// the weekly Telegram digest and by /api/admin/quality-audit (week view).
type WeeklyQualityStats struct {
	Days               int                  `json:"days"`
	Impossible         map[string]int       `json:"impossible"`     // metric → count flagged in window
	Suspect            map[string]int       `json:"suspect"`        // metric → count flagged in window
	MissedSleepNights  []string             `json:"missed_nights"`  // YYYY-MM-DD
	WatchOffHoursTotal int                  `json:"watch_off_hours"`// HRV/RHR silence aggregated
}

// HasFindings is true when there's anything worth reporting. The weekly digest
// uses this to suppress empty reports.
func (s WeeklyQualityStats) HasFindings() bool {
	return len(s.Impossible) > 0 || len(s.Suspect) > 0 ||
		len(s.MissedSleepNights) > 0 || s.WatchOffHoursTotal > 0
}

// WeeklyQualityReport gathers all data-quality findings over the trailing
// `days` days. Read-only; safe to call any time.
func (s *DB) WeeklyQualityReport(days int) (WeeklyQualityStats, error) {
	if days <= 0 {
		days = 7
	}
	stats := WeeklyQualityStats{
		Days:       days,
		Impossible: map[string]int{},
		Suspect:    map[string]int{},
	}
	ctx, cancel := queryCtx()
	defer cancel()

	// Per-metric impossible / suspect counts in the window. One query, group
	// by (metric_name, quality).
	rows, err := s.pool.Query(ctx, `
		SELECT metric_name, quality, COUNT(*)
		  FROM metric_points
		 WHERE quality IN ('impossible','suspect')
		   AND SUBSTRING(date,1,10) >= TO_CHAR(NOW() - INTERVAL '1 day' * $1, 'YYYY-MM-DD')
		 GROUP BY metric_name, quality`,
		days)
	if err != nil {
		return stats, err
	}
	for rows.Next() {
		var name, quality string
		var count int
		if err := rows.Scan(&name, &quality, &count); err != nil {
			continue
		}
		switch quality {
		case "impossible":
			stats.Impossible[name] = count
		case "suspect":
			stats.Suspect[name] = count
		}
	}
	rows.Close()

	// Missed sleep nights: dates within the window that have no sleep_total
	// row at all. We reconstruct the expected list from now-N to now and diff
	// against what's recorded. Using server local date since users live in one
	// timezone per tenant.
	now := time.Now()
	expected := map[string]bool{}
	for i := 1; i <= days; i++ {
		d := now.AddDate(0, 0, -i).Format("2006-01-02")
		expected[d] = true
	}
	rows2, err := s.pool.Query(ctx, `
		SELECT DISTINCT SUBSTRING(date,1,10)
		  FROM metric_points
		 WHERE metric_name = 'sleep_total'
		   AND quality     = 'ok'
		   AND qty > 0
		   AND SUBSTRING(date,1,10) >= TO_CHAR(NOW() - INTERVAL '1 day' * $1, 'YYYY-MM-DD')`,
		days)
	if err == nil {
		for rows2.Next() {
			var d string
			if rows2.Scan(&d) == nil {
				delete(expected, d)
			}
		}
		rows2.Close()
	}
	for d := range expected {
		stats.MissedSleepNights = append(stats.MissedSleepNights, d)
	}
	// Sort ascending so the digest reads chronologically.
	sortStrings(stats.MissedSleepNights)

	// Watch-off hours: rough estimate as gap between most recent HRV write and
	// "now", capped at the window. Doesn't try to find inner gaps — that's
	// what /api/admin/gaps already covers; the digest just wants a number to
	// signal "your watch was off a lot this week".
	var lastHRV *string
	s.pool.QueryRow(ctx,
		`SELECT MAX(date) FROM metric_points WHERE metric_name = 'heart_rate_variability'`,
	).Scan(&lastHRV)
	if lastHRV != nil {
		if t, err := parseMetricDate(*lastHRV); err == nil {
			gap := time.Since(t)
			if gap > time.Duration(days)*24*time.Hour {
				gap = time.Duration(days) * 24 * time.Hour
			}
			if gap > 6*time.Hour {
				stats.WatchOffHoursTotal = int(gap.Hours())
			}
		}
	}

	return stats, nil
}

// sortStrings is a tiny in-place sort kept here so this file doesn't pull in
// "sort" just for one call. Stable insertion sort, fine for the dozens of
// dates in scope.
func sortStrings(a []string) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}
