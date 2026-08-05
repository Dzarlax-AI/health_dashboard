package storage

import (
	"sort"
	"time"
)

// GetTypicalWakeTime returns the median wake-time-of-day computed from the
// last `days` canonical wake_time rows. Until seven canonical rows exist it
// falls back to the legacy sleep-stage-start heuristic below.
//
// Sleep records are written with the segment START timestamp
// (internal/applehealth/parse.go: Apple HealthKit semantics), so a single
// calendar day D contains BOTH the early-morning wake segments (00:00-07:00,
// continuation of the night that started D-1 22:00) AND the evening bedtime
// segments (22:00-23:59 of the next night). A naive MAX(HH:MM) per day picks
// the bedtime, not the wake. We bound the upper end at 13:00 so evening
// bedtime starts are excluded from the per-day MAX; the >= 04:00 floor
// excludes short night-time naps. This window also tolerates moderate
// late risers without distorting the median.
//
// Returns (hour, minute, ok=true) when either source has at least 7 valid
// days; otherwise (0, 0, false) so callers can fall back to a static cap.
func (s *DB) GetTypicalWakeTime(days int, locations ...*time.Location) (int, int, bool) {
	if days <= 0 {
		days = 14
	}
	loc := time.Local
	if len(locations) > 0 && locations[0] != nil {
		loc = locations[0]
	}
	beforeDate := time.Now().In(loc).Format("2006-01-02")
	if minutes, ok, err := s.typicalDerivedWakeMinutes(beforeDate, days, loc); err == nil && ok {
		return minutes / 60, minutes % 60, true
	}

	// Compatibility fallback for installs that have not accumulated seven
	// canonical wake_time rows yet. This path is intentionally temporary and
	// keeps the previous stage-start heuristic until backfill completes.
	ctx, cancel := queryCtx()
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		SELECT MAX(SUBSTRING(date, 12, 5)) AS wake_hhmm
		  FROM metric_points
		 WHERE metric_name IN ('sleep_total','sleep_core','sleep_rem','sleep_deep','sleep_unspecified','sleep_awake')
		   AND quality = 'ok'
		   AND SUBSTRING(date, 12, 8) <> '00:00:00'
		   AND SUBSTRING(date, 12, 5) >= '04:00'
		   AND SUBSTRING(date, 12, 5) <= '13:00'
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
