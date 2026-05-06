package storage

import (
	"database/sql"
	"time"
)

// SleepSettleStatus describes whether the morning report can be safely sent yet.
// "ok" means data is settled. Any non-"ok" reason means caller should defer
// (or force-send with a warning if the deadline has been reached).
type SleepSettleStatus struct {
	Settled bool
	Reason  string // "ok" | "no_data" | "recent_segment" | "still_writing"
	// Diagnostic fields, populated when available.
	LatestSegmentEnd time.Time
	LatestIngest     time.Time
}

// Tunables for the settle gates. Exposed as package-level so tests can lower
// them, but kept const for production. Numbers are conservative on purpose:
// the cost of waiting an extra few minutes is low; the cost of sending a
// half-baked morning report (e.g. user got up to walk the dog and went back
// to sleep) is a confused user.
const (
	settleSegmentGap = 45 * time.Minute // how long since last sleep fragment ended
	settleIngestGap  = 20 * time.Minute // how long since last sleep fragment was ingested
)

// SleepSettled reports whether last night's sleep_total data for the given
// calendar date (YYYY-MM-DD) is stable enough to base a morning report on.
//
// Three gates, in order:
//  1. At least one sleep_total record exists for the date.
//  2. Latest segment ended ≥ settleSegmentGap ago. Guards against the
//     wake-walk-dog-sleep-again case: if the watch is recording another sleep
//     segment right now, we'd be reporting an incomplete picture.
//  3. No new fragments ingested in ≥ settleIngestGap. Apple Watch sometimes
//     uploads sleep fragments out of order or in trickles; once the trickle
//     stops we're safe to read.
//
// All comparisons use the server's wall clock; metric_points.date is TEXT
// formatted as "YYYY-MM-DD HH:MM:SS ±TZ" and parses cleanly into time.Time.
func (s *DB) SleepSettled(date string) SleepSettleStatus {
	ctx, cancel := queryCtx()
	defer cancel()

	var latestEndStr sql.NullString
	var latestRecv sql.NullTime
	err := s.pool.QueryRow(ctx, `
		SELECT MAX(date), MAX(received_at)
		  FROM metric_points
		 WHERE metric_name = 'sleep_total'
		   AND SUBSTRING(date, 1, 10) = $1
	`, date).Scan(&latestEndStr, &latestRecv)

	if err != nil || !latestEndStr.Valid {
		return SleepSettleStatus{Reason: "no_data"}
	}

	latestEnd, perr := parseMetricDate(latestEndStr.String)
	if perr != nil {
		// If we can't parse the timestamp, fall back to ingest time only.
		// Bail conservatively rather than silently shipping a stale report.
		return SleepSettleStatus{Reason: "no_data"}
	}

	now := time.Now()
	st := SleepSettleStatus{LatestSegmentEnd: latestEnd}
	if latestRecv.Valid {
		st.LatestIngest = latestRecv.Time
	}

	if now.Sub(latestEnd) < settleSegmentGap {
		st.Reason = "recent_segment"
		return st
	}
	if latestRecv.Valid && now.Sub(latestRecv.Time) < settleIngestGap {
		st.Reason = "still_writing"
		return st
	}
	st.Settled = true
	st.Reason = "ok"
	return st
}

// parseMetricDate accepts the formats observed in metric_points.date.
// Apple Health export uses "2006-01-02 15:04:05 -0700"; the Go time package
// recognises this with the layout below. We also accept RFC3339 just in case.
func parseMetricDate(s string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05 -0700",
		"2006-01-02 15:04:05 -07:00",
		time.RFC3339,
	}
	var lastErr error
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, lastErr
}
