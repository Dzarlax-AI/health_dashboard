package storage

import (
	"database/sql"
	"log"
	"os"
	"time"
)

// ComputeOvernightHRBaseline returns the median heart rate over the last
// 3 hours of the main sleep segment ending on `date`. This is the v2.2
// stress-methodology canonical "RHR baseline" — replaces the
// `daily_scores.rhr_avg` column which is a per-day average of heart_rate
// (NOT an overnight resting baseline; see STRESS_MEASUREMENT.md §0
// blocker 1).
//
// The window is anchored on `WakeTimeForDate(date, loc)` (which already
// applies the longest-asleep-segment-within-±6h-of-midnight rule from
// §0 blocker 2). When the wake-time resolver returns imputed/ok=false
// (no main sleep found — pulled all-nighter, watch off, etc.), this
// function falls back to a fixed 03:00–06:00 window of `date` and
// returns imputed=true.
//
// Returns (median, imputed, ok). ok=false on hard DB error, malformed
// `date`, OR when no `quality='ok'` heart_rate samples exist in the
// resolved window even after fallback — callers should treat ok=false
// as "leave the daily_scores column NULL".
//
// loc must be the tenant's REPORT_TZ. Same convention as
// WakeTimeForDate — caller resolves TZ once, threads it through.
func (s *DB) ComputeOvernightHRBaseline(date string, loc *time.Location) (median float64, imputed bool, ok bool) {
	if loc == nil {
		loc = time.UTC
	}
	d, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return 0, false, false
	}

	wakeHour, _, wakeImputed, wakeOK := s.WakeTimeForDate(date, loc)
	start, end, imputed := resolveBaselineWindow(d, wakeHour, wakeImputed, wakeOK, loc)

	// metric_points.date is TEXT in "YYYY-MM-DD HH:MM:SS ±TZ" format —
	// lexicographic compare is correct for ISO timestamps. Format the
	// window bounds in the same shape (same %Y-format, +TZ offset)
	// against the tenant's loc.
	const dateLayout = "2006-01-02 15:04:05 -0700"
	startStr := start.In(loc).Format(dateLayout)
	endStr := end.In(loc).Format(dateLayout)

	ctx, cancel := queryCtx()
	defer cancel()
	var got sql.NullFloat64
	// percentile_cont(0.5) WITHIN GROUP gives true median (PostgreSQL
	// 11+). `qty` on heart_rate metric_points rows is the HR per
	// sample (HAE / health-sync ship per-minute samples). Across-source
	// median is acceptable for the ~60–100 samples in a 3h window —
	// individual-source skew is small relative to natural beat-to-beat
	// variation, and quality='ok' already strips suspect/impossible
	// outliers.
	err = s.pool.QueryRow(ctx, `
		SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY qty)
		  FROM metric_points
		 WHERE metric_name = 'heart_rate'
		   AND quality = 'ok'
		   AND date >= $1
		   AND date <  $2`, startStr, endStr).Scan(&got)
	if err != nil {
		log.Printf("ComputeOvernightHRBaseline %s: %v", date, err)
		return 0, imputed, false
	}
	if !got.Valid || !isFiniteFloat(got.Float64) {
		return 0, imputed, false
	}
	return got.Float64, imputed, true
}

// UpsertBaselineHROvernightForDate is the exported wrapper around the
// internal writer — used by `cmd/baseline_check --apply` and any future
// one-off backfill cmd. Production cache rebuilds call the lowercase
// variant from inside cacheMu in `UpsertRecentCache`.
func (s *DB) UpsertBaselineHROvernightForDate(date string, loc *time.Location) {
	s.upsertBaselineHROvernightForDate(date, loc)
}

// upsertBaselineHROvernightForDate computes the v2.2 baseline and
// writes it to daily_scores.baseline_hr_overnight. Idempotent — runs
// after `upsertDailyForDate` from inside the same cacheMu critical
// section. When the helper returns ok=false (no sleep AND no fallback
// HR), the column stays NULL via the conditional UPDATE — we never
// overwrite a valid prior value with NULL.
func (s *DB) upsertBaselineHROvernightForDate(date string, loc *time.Location) {
	median, _, ok := s.ComputeOvernightHRBaseline(date, loc)
	if !ok {
		return
	}
	ctx, cancel := queryCtx()
	defer cancel()
	if _, err := s.pool.Exec(ctx, `
		UPDATE daily_scores
		   SET baseline_hr_overnight = $2
		 WHERE date = $1`, date, median); err != nil {
		log.Printf("upsertBaselineHROvernightForDate %s: %v", date, err)
	}
}

// reportTZLocation loads the tenant's REPORT_TZ as `time.Location`.
// Falls back to UTC on missing/invalid env to match the rest of the
// codebase (energy_compute.go etc.) — keeps multi-tenant rollout
// non-fatal even when one tenant's TZ env is misconfigured.
func reportTZLocation() *time.Location {
	tz := os.Getenv("REPORT_TZ")
	if tz == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(tz)
	if err != nil || loc == nil {
		return time.UTC
	}
	return loc
}

// resolveBaselineWindow picks the [start, end) interval for the
// "last 3h of main sleep" baseline.
//
//   - Happy path: wake-time resolver succeeded → end = wake_time of
//     date d, start = wake - 3h. The window catches the resting tail
//     of the night where HR is lowest and most stable.
//
//   - Imputed path: wake-time resolver fell back (`wakeImputed=true`)
//     or failed (`wakeOK=false`) → fixed 03:00–06:00 window of date d.
//     This is the same default the original `rhr_avg` column used
//     before per-segment sleep data; correct-ish for typical
//     bedtimes, wrong for shift work / jet-lag / late-night. The
//     `imputed=true` return surfaces the degradation to callers.
//
// Pure function — no DB access, no env reads — so unit tests can pin
// the window math against fabricated inputs.
func resolveBaselineWindow(
	d time.Time,
	wakeHour float64,
	wakeImputed, wakeOK bool,
	loc *time.Location,
) (start, end time.Time, imputed bool) {
	if !wakeOK || wakeImputed {
		start = time.Date(d.Year(), d.Month(), d.Day(), 3, 0, 0, 0, loc)
		end = time.Date(d.Year(), d.Month(), d.Day(), 6, 0, 0, 0, loc)
		return start, end, true
	}
	wakeH := int(wakeHour)
	wakeM := int((wakeHour - float64(wakeH)) * 60)
	end = time.Date(d.Year(), d.Month(), d.Day(), wakeH, wakeM, 0, 0, loc)
	start = end.Add(-3 * time.Hour)
	return start, end, false
}

// isFiniteFloat is a tiny guard against NaN/±Inf from pathological
// aggregates — saves a math import in the call sites that consume
// percentile output.
func isFiniteFloat(v float64) bool {
	return v == v && v < 1e308 && v > -1e308
}
