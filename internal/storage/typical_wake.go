package storage

import (
	"sort"
)

// GetTypicalWakeTime returns the median wake-time-of-day computed from the
// last `days` calendar days of sleep_* records. wake_time per day = MAX(date)
// across sleep_* metrics, restricted to records whose time-of-day is not
// '00:00:00' (HAE-style daily summaries carry no wake info) and is after
// 04:00 (filters short night-time naps that would skew toward early morning).
//
// Returns (hour, minute, ok=true) when at least 7 valid days are available;
// otherwise (0, 0, false) so callers can fall back to a static cap. Threshold
// of 7 keeps the median stable against a single oversleep/undersleep day.
func (s *DB) GetTypicalWakeTime(days int) (int, int, bool) {
	if days <= 0 {
		days = 14
	}
	ctx, cancel := queryCtx()
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		SELECT MAX(SUBSTRING(date, 12, 5)) AS wake_hhmm
		  FROM metric_points
		 WHERE metric_name IN ('sleep_total','sleep_core','sleep_rem','sleep_deep','sleep_awake')
		   AND quality = 'ok'
		   AND SUBSTRING(date, 12, 8) <> '00:00:00'
		   AND SUBSTRING(date, 12, 5) >= '04:00'
		   AND SUBSTRING(date, 1, 10) >= TO_CHAR(NOW() - INTERVAL '1 day' * $1, 'YYYY-MM-DD')
		 GROUP BY SUBSTRING(date, 1, 10)`, days)
	if err != nil {
		return 0, 0, false
	}
	defer rows.Close()

	var minutes []int
	for rows.Next() {
		var hhmm string
		if err := rows.Scan(&hhmm); err != nil || len(hhmm) != 5 {
			continue
		}
		h := (int(hhmm[0]-'0'))*10 + int(hhmm[1]-'0')
		m := (int(hhmm[3]-'0'))*10 + int(hhmm[4]-'0')
		if h < 0 || h > 23 || m < 0 || m > 59 {
			continue
		}
		minutes = append(minutes, h*60+m)
	}
	if len(minutes) < 7 {
		return 0, 0, false
	}
	sort.Ints(minutes)
	med := minutes[len(minutes)/2]
	return med / 60, med % 60, true
}
