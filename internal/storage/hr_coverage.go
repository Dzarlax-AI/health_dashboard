package storage

import (
	"database/sql"
	"log"
	"time"
)

// HRCoverageHours returns the number of distinct hours within the
// awake window for `date` that carry at least one `quality='ok'`
// heart_rate sample. STRESS_MEASUREMENT.md §0 blocker 3 and §4.4 use
// this to gate the v2.2 stress drain: when coverage < MIN_COVERAGE
// hours (8h by default) the day's `sustained_hr_load` is unreliable
// (watch off charger, sync gap, etc.) and the formula must fall back
// to v2.0 (kcal-only) drain with a `stale_stress` flag.
//
// The awake window is resolved via `WakeTimeForDate(date, loc)`:
//
//   - Happy path: wake at morning of d, onset at evening of d.
//     Window is contiguous within d: [d.wakeHour, d.onsetHour).
//
//   - Late-bedtime path: onsetHour < wakeHour (numerically) means
//     onset falls past midnight into d+1. Window crosses midnight:
//     [d.wakeHour, d+1.onsetHour).
//
//   - Imputed path: wake-time resolver failed → fixed 07:00–22:00
//     fallback. Still a single contiguous window within d.
//
// Counted distinct hours use the metric_points `date` text column's
// "YYYY-MM-DD HH" prefix — same bucketing the rest of the codebase
// uses for hourly aggregates. An hour with even one HR sample counts;
// the spec doesn't require minimum density at this granularity
// because §4.4's `hour_hr[h]` is a separate per-hour median that
// already enforces per-hour validity.
//
// Returns (hours, ok). ok=false on hard DB error or unparseable
// `date` — callers should treat as "coverage unknown" and gate
// stress drain conservatively (i.e. fall back to v2.0).
//
// `loc` must be the tenant's REPORT_TZ — same convention as
// WakeTimeForDate / ComputeOvernightHRBaseline.
func (s *DB) HRCoverageHours(date string, loc *time.Location) (int, bool) {
	if loc == nil {
		loc = time.UTC
	}
	d, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return 0, false
	}

	wakeHour, onsetHour, _, _ := s.WakeTimeForDate(date, loc)
	awakeStart, awakeEnd := resolveAwakeBounds(d, wakeHour, onsetHour, loc)

	const dateLayout = "2006-01-02 15:04:05 -0700"
	startStr := awakeStart.In(loc).Format(dateLayout)
	endStr := awakeEnd.In(loc).Format(dateLayout)

	ctx, cancel := queryCtx()
	defer cancel()
	var hours sql.NullInt64
	err = s.pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT SUBSTRING(date, 1, 13))
		  FROM metric_points
		 WHERE metric_name = 'heart_rate'
		   AND quality = 'ok'
		   AND date >= $1
		   AND date <  $2`, startStr, endStr).Scan(&hours)
	if err != nil {
		log.Printf("HRCoverageHours %s: %v", date, err)
		return 0, false
	}
	if !hours.Valid {
		return 0, true
	}
	return int(hours.Int64), true
}

// AwakeWindowBounds resolves the absolute [start, end) timestamps for
// the awake window of `date` in the tenant's TZ. Same wake/onset
// resolution as HRCoverageHours, exposed so callers can ask "is this
// day still in progress?" (`time.Now().In(loc).Before(end)`) without
// re-parsing the date or re-running WakeTimeForDate.
//
// Returns ok=false on unparseable date — caller decides the
// degraded-data response.
func (s *DB) AwakeWindowBounds(date string, loc *time.Location) (start, end time.Time, ok bool) {
	if loc == nil {
		loc = time.UTC
	}
	d, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return time.Time{}, time.Time{}, false
	}
	wakeHour, onsetHour, _, _ := s.WakeTimeForDate(date, loc)
	start, end = resolveAwakeBounds(d, wakeHour, onsetHour, loc)
	return start, end, true
}

// resolveAwakeBounds turns the [wakeHour, onsetHour) hour-of-day pair
// returned by WakeTimeForDate into absolute [start, end) timestamps
// for date d. Handles the cross-midnight case (onset < wake → onset
// falls on d+1) so a single SQL range catches the entire awake
// interval without UNION'ing two queries.
//
// Pure function, no DB access — pinned by unit tests against
// fabricated inputs.
func resolveAwakeBounds(
	d time.Time,
	wakeHour, onsetHour float64,
	loc *time.Location,
) (start, end time.Time) {
	wH, wM := splitHour(wakeHour)
	oH, oM := splitHour(onsetHour)
	start = time.Date(d.Year(), d.Month(), d.Day(), wH, wM, 0, 0, loc)
	endDate := d
	if onsetHour < wakeHour {
		// Cross-midnight: onset is the next calendar day.
		endDate = d.AddDate(0, 0, 1)
	}
	end = time.Date(endDate.Year(), endDate.Month(), endDate.Day(), oH, oM, 0, 0, loc)
	return start, end
}

// splitHour decomposes a float hour-of-day into integer hour and
// minute. Truncates seconds — the awake window doesn't need sub-
// minute resolution and the WakeTimeForDate output is already
// quantised to per-segment sample boundaries.
func splitHour(h float64) (hour, minute int) {
	hour = int(h)
	if hour < 0 {
		hour = 0
	}
	if hour > 23 {
		hour = 23
	}
	minute = int((h - float64(int(h))) * 60)
	if minute < 0 {
		minute = 0
	}
	if minute > 59 {
		minute = 59
	}
	return hour, minute
}
