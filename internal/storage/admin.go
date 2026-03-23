package storage

import (
	"context"
	"log"
	"time"
)

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

// DataGap represents a contiguous range of dates with missing or incomplete health data.
type DataGap struct {
	From         string `json:"from"`                   // first affected date (YYYY-MM-DD)
	To           string `json:"to"`                     // last affected date (YYYY-MM-DD)
	Days         int    `json:"days"`                   // number of affected days
	Partial      bool   `json:"partial,omitempty"`      // data exists but fewer than minHours recorded
	TodayMissing bool   `json:"today_missing,omitempty"` // no data received for today yet
}

// GetDataGaps returns date ranges where health data is missing or sparse in hourly_metrics.
// Looks back 12 months. Two kinds of gaps are detected:
//   - Complete: consecutive days with zero entries (≥ minGapDays)
//   - Partial: individual days with fewer than minHours distinct hours of data
func (s *DB) GetDataGaps(minGapDays, minHours int) ([]DataGap, error) {
	if minGapDays <= 0 {
		minGapDays = 2
	}
	if minHours <= 0 {
		minHours = 6
	}

	// Compute the 12-months-ago cutoff in Go and pass as parameter.
	twelveMonthsAgo := time.Now().AddDate(-1, 0, 0).Format("2006-01-02")

	// Fetch all days in the last 12 months with their hour counts.
	ctx := context.Background()
	rows, err := s.pool.Query(ctx, `
		SELECT SUBSTRING(hour,1,10) AS day, COUNT(DISTINCT SUBSTRING(hour,1,13)) AS hours
		FROM hourly_metrics
		WHERE SUBSTRING(hour,1,10) >= $1
		GROUP BY SUBSTRING(hour,1,10)
		ORDER BY SUBSTRING(hour,1,10)`, twelveMonthsAgo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type dayInfo struct {
		date  string
		hours int
	}
	var days []dayInfo
	for rows.Next() {
		var di dayInfo
		if rows.Scan(&di.date, &di.hours) == nil {
			days = append(days, di)
		}
	}
	if len(days) == 0 {
		return nil, nil
	}

	// Build a set of days that exist and their hour counts.
	dayMap := make(map[string]int, len(days))
	for _, d := range days {
		dayMap[d.date] = d.hours
	}

	minDate, _ := time.Parse("2006-01-02", days[0].date)
	maxDate, _ := time.Parse("2006-01-02", days[len(days)-1].date)
	// Don't flag today as partial — it's still in progress.
	today := time.Now().Format("2006-01-02")

	var gaps []DataGap

	// Walk every calendar day in [minDate, yesterday] looking for complete gaps.
	// We stop at yesterday because today might still be in progress.
	yesterday := time.Now().AddDate(0, 0, -1)
	walkEnd := maxDate
	if yesterday.Before(walkEnd) {
		walkEnd = yesterday
	}
	cur := minDate
	for !cur.After(walkEnd) {
		d := cur.Format("2006-01-02")
		if _, ok := dayMap[d]; !ok {
			// Start of a complete-missing run.
			runStart := cur
			for !cur.After(walkEnd) {
				d = cur.Format("2006-01-02")
				if _, ok := dayMap[d]; ok {
					break
				}
				cur = cur.AddDate(0, 0, 1)
			}
			runEnd := cur.AddDate(0, 0, -1)
			runDays := int(runEnd.Sub(runStart).Hours()/24) + 1
			if runDays >= minGapDays {
				gaps = append(gaps, DataGap{
					From: runStart.Format("2006-01-02"),
					To:   runEnd.Format("2006-01-02"),
					Days: runDays,
				})
			}
			continue
		}
		cur = cur.AddDate(0, 0, 1)
	}

	// Trailing gap: check if recent days (up to yesterday) have no data at all.
	if len(days) > 0 {
		lastT, _ := time.Parse("2006-01-02", days[len(days)-1].date)
		trailingDays := int(yesterday.Sub(lastT).Hours() / 24)
		if trailingDays >= minGapDays {
			gaps = append(gaps, DataGap{
				From: lastT.AddDate(0, 0, 1).Format("2006-01-02"),
				To:   yesterday.Format("2006-01-02"),
				Days: trailingDays,
			})
		}
	}

	// Partial days: present in hourly_metrics but with fewer than minHours hours.
	for _, di := range days {
		if di.date == today {
			continue
		}
		if di.hours < minHours {
			gaps = append(gaps, DataGap{
				From:    di.date,
				To:      di.date,
				Days:    1,
				Partial: true,
			})
		}
	}

	// Today: flag if no data at all has been received yet.
	if _, hasToday := dayMap[today]; !hasToday {
		gaps = append(gaps, DataGap{
			From:         today,
			To:           today,
			Days:         1,
			TodayMissing: true,
		})
	}

	return gaps, nil
}

// InvalidateDateRangeAggregates deletes all pre-aggregated rows for [from, to]
// (inclusive, YYYY-MM-DD) so that the next backfill recomputes them from metric_points.
func (s *DB) InvalidateDateRangeAggregates(from, to string) {
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx,
		"DELETE FROM hourly_metrics WHERE SUBSTRING(hour,1,10) >= $1 AND SUBSTRING(hour,1,10) <= $2", from, to,
	); err != nil {
		log.Printf("invalidate hourly_metrics [%s,%s]: %v", from, to, err)
	}
	if _, err := s.pool.Exec(ctx, "DELETE FROM daily_scores WHERE date >= $1 AND date <= $2", from, to); err != nil {
		log.Printf("invalidate daily_scores [%s,%s]: %v", from, to, err)
	}
}

// GetCacheStatus returns row counts and date ranges for all cache tables.
func (s *DB) GetCacheStatus() (*CacheStatus, error) {
	ctx := context.Background()
	cs := &CacheStatus{ScoreVersion: ScoreVersion}

	s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM metric_points`).Scan(&cs.RawPoints.Rows)
	s.pool.QueryRow(ctx, `SELECT COUNT(DISTINCT metric_name) FROM hourly_metrics`).Scan(&cs.RawPoints.Metrics)

	// minute_metrics is no longer used (reads go to metric_points directly).
	// Keep the struct field for API compat but leave it zeroed.

	s.pool.QueryRow(ctx,
		`SELECT COUNT(*), MIN(hour), MAX(hour) FROM hourly_metrics`,
	).Scan(&cs.HourlyCache.Rows, &cs.HourlyCache.Oldest, &cs.HourlyCache.Newest)

	s.pool.QueryRow(ctx,
		`SELECT COUNT(*), MIN(date), MAX(date) FROM daily_scores`,
	).Scan(&cs.DailyScores.Rows, &cs.DailyScores.Oldest, &cs.DailyScores.Newest)

	s.pool.QueryRow(ctx,
		`SELECT MAX(received_at) FROM health_records`,
	).Scan(&cs.LastSync)

	return cs, nil
}
