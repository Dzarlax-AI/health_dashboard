package storage

import (
	"log"
	"time"
)

// HourlyHRStat is one row of the per-hour HR series that feeds v2.2
// `sustained_hr_load` (STRESS_MEASUREMENT.md §4.4).
//
//   - Median5minMinHR: median of per-5-minute minima within the hour.
//     Per the spec, taking 5-min minima first kills postural-noise
//     spikes (a Watch sample at 95 bpm during a stand-up doesn't drag
//     the hour's "resting tone"); the median over those minima is
//     robust to the remaining few-bpm jitter.
//   - SampleCount: raw HR samples in the hour after `quality='ok'`
//     filter.
//   - Buckets5Min: distinct 5-min buckets with at least one sample.
//     Max 12; below `MinBucketsPerHour` the hour is too sparse for a
//     stable median and CoverageOK is false.
//   - CoverageOK: Buckets5Min >= MinBucketsPerHour. Caller aggregates
//     coverage_ok=true rows for the §4.4 MIN_COVERAGE = 8h gate.
type HourlyHRStat struct {
	Hour            time.Time
	Median5minMinHR float64
	SampleCount     int
	Buckets5Min     int
	CoverageOK      bool
}

// MinBucketsPerHour is the per-hour density threshold below which the
// median-of-5-min-minima is statistically unreliable. 3 of 12 possible
// buckets = at least 15 minutes of HR coverage in the hour, enough for
// a stable median. Bumping this tightens what counts as "real" hours
// for the §4.4 sustained-load integral.
const MinBucketsPerHour = 3

// HourlyHRSeriesForAwakeWindow returns one row per hour in the awake
// window for `date`. Hours with no HR samples are emitted with
// SampleCount=0 / CoverageOK=false so the caller sees the full
// awake-window shape without having to plumb gaps.
//
// Awake window is resolved via `WakeTimeForDate(date, loc)` — same
// source of truth as `HRCoverageHours` and the upcoming §4.4
// SustainedHRLoad consumer.
//
// Returns (rows, ok). ok=false on hard DB error or unparseable
// `date`. An empty slice with ok=true means the awake window is
// zero-width (degenerate wake==onset) — caller should fall back to
// v2.0 drain for the day.
//
// `loc` must be the tenant's REPORT_TZ.
func (s *DB) HourlyHRSeriesForAwakeWindow(date string, loc *time.Location) ([]HourlyHRStat, bool) {
	if loc == nil {
		loc = time.UTC
	}
	d, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return nil, false
	}

	wakeHour, onsetHour, _, _ := s.WakeTimeForDate(date, loc)
	awakeStart, awakeEnd := resolveAwakeBounds(d, wakeHour, onsetHour, loc)
	if !awakeEnd.After(awakeStart) {
		return []HourlyHRStat{}, true
	}

	const dateLayout = "2006-01-02 15:04:05 -0700"
	startStr := awakeStart.In(loc).Format(dateLayout)
	endStr := awakeEnd.In(loc).Format(dateLayout)

	// Two-stage bucketing:
	//   1. Each row → 5-min bucket id = date_trunc('hour') + floor(min/5)*5
	//      GROUP BY (hour_bucket, bucket5) → MIN(qty) per 5-min slice,
	//      and a per-bucket sample count.
	//   2. GROUP BY hour_bucket → percentile_cont over per-bucket minima
	//      for Median5minMinHR; COUNT(distinct bucket5) for Buckets5Min;
	//      SUM(samples) for total SampleCount.
	//
	// `date::timestamptz` parses the TEXT date column ("YYYY-MM-DD
	// HH:MM:SS ±TZ" — Apple Health convention). Postgres handles the
	// offset correctly; date_trunc('hour') then operates in UTC, but
	// we re-attach loc when scanning into Go.
	ctx, cancel := longCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		WITH ts_samples AS (
		    SELECT date::timestamptz AS ts, qty
		      FROM metric_points
		     WHERE metric_name = 'heart_rate'
		       AND quality = 'ok'
		       AND date >= $1
		       AND date <  $2
		),
		five_min AS (
		    SELECT
		        date_trunc('hour', ts) AS hour_bucket,
		        date_trunc('hour', ts)
		            + (FLOOR(EXTRACT(MINUTE FROM ts) / 5) * INTERVAL '5 minutes') AS bucket5,
		        MIN(qty) AS bucket_min,
		        COUNT(*) AS bucket_samples
		      FROM ts_samples
		     GROUP BY 1, 2
		)
		SELECT
		    hour_bucket,
		    percentile_cont(0.5) WITHIN GROUP (ORDER BY bucket_min) AS median_min,
		    COUNT(*) AS buckets_5min,
		    SUM(bucket_samples)::INT AS sample_count
		  FROM five_min
		 GROUP BY hour_bucket
		 ORDER BY hour_bucket`, startStr, endStr)
	if err != nil {
		log.Printf("HourlyHRSeriesForAwakeWindow %s: %v", date, err)
		return nil, false
	}
	defer rows.Close()

	stats := make(map[time.Time]HourlyHRStat)
	for rows.Next() {
		var hour time.Time
		var med float64
		var buckets, samples int
		if err := rows.Scan(&hour, &med, &buckets, &samples); err != nil {
			log.Printf("HourlyHRSeriesForAwakeWindow scan %s: %v", date, err)
			continue
		}
		hour = hour.In(loc).Truncate(time.Hour)
		stats[hour] = HourlyHRStat{
			Hour:            hour,
			Median5minMinHR: med,
			SampleCount:     samples,
			Buckets5Min:     buckets,
			CoverageOK:      buckets >= MinBucketsPerHour,
		}
	}
	if err := rows.Err(); err != nil {
		log.Printf("HourlyHRSeriesForAwakeWindow iter %s: %v", date, err)
		return nil, false
	}

	// Zero-fill hours with no data so the returned slice matches the
	// awake-window length exactly. Caller can iterate without
	// special-casing gaps.
	slots := emitHourSlots(awakeStart, awakeEnd, loc)
	out := make([]HourlyHRStat, 0, len(slots))
	for _, slot := range slots {
		if got, ok := stats[slot]; ok {
			out = append(out, got)
			continue
		}
		out = append(out, HourlyHRStat{Hour: slot, CoverageOK: false})
	}
	return out, true
}

// emitHourSlots returns the sequence of hour-boundary timestamps in
// [start, end), each in `loc`. start is rounded down to its hour;
// end is exclusive — an hour that begins exactly at end is NOT
// included. Pure function — unit-testable without DB.
func emitHourSlots(start, end time.Time, loc *time.Location) []time.Time {
	if loc == nil {
		loc = time.UTC
	}
	startH := start.In(loc).Truncate(time.Hour)
	out := []time.Time{}
	for t := startH; t.Before(end); t = t.Add(time.Hour) {
		out = append(out, t)
	}
	return out
}
