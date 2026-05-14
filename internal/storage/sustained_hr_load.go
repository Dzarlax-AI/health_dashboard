package storage

import (
	"log"
	"math"
	"time"

	"health-receiver/internal/health"
)

// SustainedHRLoadResult is the §4.4 stress-drain output for one date —
// computed by orchestrating WakeTimeForDate + HRCoverageHours +
// PersonalBaseline(hr_awake) + HourlyHRSeriesForAwakeWindow and feeding
// the resulting per-hour z-series through health.SustainedHRLoad.
//
//   - SustainedHRLoadZ: dimensionless z-load that feeds the
//     β · sustained_hr_load term in DrainV2. Higher = more autonomic
//     load. Returns 0 when any prerequisite fails (coverage gate,
//     calibration state, missing baseline).
//
//   - HROvershootBpmHours: raw bpm·hours sum — Σ max(0, hour_hr[h] −
//     baseline_hr_awake) over the awake window. Stored alongside the
//     z-load in components JSONB so users can hear "HR ran ~8 bpm
//     above your normal for ~4 hours" without back-converting z-units.
//
//   - Flags: subset of {stale_stress, calibration_warmup,
//     no_per_segment_sleep} — surfaces why the day was skipped/derated
//     to PR-9's verdict layer. Empty when everything is steady.
//
// Returns ok=false on hard DB error or unparseable date — caller writes
// NULL to daily_scores.sustained_hr_load. ok=true with Z=0 happens when
// a gate (coverage, calibration) fires — the day is "honestly zero",
// not "errored".
type SustainedHRLoadResult struct {
	SustainedHRLoadZ    float64
	HROvershootBpmHours float64
	Flags               []string
}

// ComputeSustainedHRLoadForDate is the §4.4 orchestrator. Pure read
// against existing helpers; no writes. Wraps `WakeTimeForDate +
// HRCoverageHours + PersonalBaseline + HourlyHRSeriesForAwakeWindow`
// and converts per-hour HR into z-load via health.HourZShift +
// health.SustainedHRLoad.
//
// Each prerequisite returns (zero, flag, ok=true) on degraded data
// rather than erroring — the formula's response to partial data should
// match its response to fully-missing data (the §4.3 stale_stress
// flag), not a crash.
//
// `loc` must be the tenant's REPORT_TZ.
func (s *DB) ComputeSustainedHRLoadForDate(
	date string,
	loc *time.Location,
) (SustainedHRLoadResult, bool) {
	if loc == nil {
		loc = time.UTC
	}
	if _, err := time.ParseInLocation("2006-01-02", date, loc); err != nil {
		return SustainedHRLoadResult{}, false
	}

	res := SustainedHRLoadResult{Flags: []string{}}

	// Coverage gate (§0 blocker 3 / §4.4 MIN_COVERAGE = 8 hours).
	// Below threshold, the per-hour HR samples don't cover enough of
	// the awake window to compute a trustworthy sustained-load
	// integral. Flag and short-circuit before the heavier baseline
	// query.
	coverage, ok := s.HRCoverageHours(date, loc)
	if !ok {
		// Hard DB error — log already done by HRCoverageHours.
		// Treat as "no data" rather than failing the orchestrator.
		res.Flags = append(res.Flags, "stale_stress")
		return res, true
	}
	if coverage < health.MinHRCoverageHours {
		res.Flags = append(res.Flags, "stale_stress")
		return res, true
	}

	// Personal baseline (§4.1). Cold state (<3 samples) gates the
	// channel; warmup state passes through with a flag so PR-9
	// verdict layer softens the narrative.
	bl, blOK := s.PersonalBaseline(date, ChannelHRAwake, 30, loc)
	if !blOK {
		res.Flags = append(res.Flags, "stale_stress")
		return res, true
	}
	if bl.State == CalibrationWarmup {
		res.Flags = append(res.Flags, "calibration_warmup")
	}

	// Per-hour HR series over the awake window. WakeTimeForDate is
	// invoked inside; imputed window → still works against fixed
	// 07:00-22:00 fallback.
	series, ok := s.HourlyHRSeriesForAwakeWindow(date, loc)
	if !ok || len(series) == 0 {
		res.Flags = append(res.Flags, "stale_stress")
		return res, true
	}

	// Convert per-hour data to z-series. Skip hours that failed the
	// per-hour coverage check (< MinBucketsPerHour 5-min buckets)
	// AND hours with no samples (zero-fill rows from
	// HourlyHRSeriesForAwakeWindow). Use a NaN placeholder so
	// health.SustainedHRLoad's NaN-tolerance kicks in and treats
	// them as gaps, not zeros.
	hourZ := make([]float64, len(series))
	for i, h := range series {
		if !h.CoverageOK {
			hourZ[i] = math.NaN()
			continue
		}
		hourZ[i] = health.HourZShift(h.Median5minMinHR, bl.Median, bl.MADSD)
	}

	// Read the live tenant config so the z-threshold is settings-
	// driven (PR-7's energy.z_threshold, default 0.5 per §4.4).
	cfg := s.GetEnergyConfig()
	res.SustainedHRLoadZ = health.SustainedHRLoad(hourZ, cfg.ZThreshold)

	// §4.3 HR-z-derived flags. Both feed PR-9 verdict layer:
	//
	//   acute_stress  — single hour z>+2 in the awake window. Per
	//                   spec, drives no behaviour ("no action —
	//                   transient"), but surfaces in components for
	//                   diagnostic UI.
	//   sustained_load — ≥4h consecutive z>+1. Real autonomic load
	//                   day; pairs with the quantitative
	//                   sustained_hr_load_z above. Distinct flag
	//                   even when SustainedHRLoadZ > 0 (a single
	//                   z=2 hour produces load but doesn't fire
	//                   the run flag).
	if health.AcuteStress(hourZ) {
		res.Flags = append(res.Flags, "acute_stress")
	}
	if health.SustainedLoadFlag(hourZ) {
		res.Flags = append(res.Flags, "sustained_load")
	}

	// HROvershootBpmHours: raw bpm·hours for the same hours that
	// contributed to the z-load. Keeps the audit-trail line in
	// human units so a future briefing can say "HR ran +8 bpm above
	// your normal for ~3 hours" — back-converting z would require
	// the user to remember their personal SD.
	var overshoot float64
	for i, h := range series {
		if !h.CoverageOK {
			continue
		}
		over := h.Median5minMinHR - bl.Median
		if over > 0 {
			overshoot += over
		}
		_ = i
	}
	res.HROvershootBpmHours = overshoot

	return res, true
}

// upsertSustainedHRLoadForDate computes the v2.2 sustained-load value
// AND the §4.3 HR-driven stress flags and writes both to
// daily_scores. NULL/empty when the orchestrator returns ok=false;
// the conditional UPDATE never overwrites a valid prior value with
// NULL.
//
// The stress_flags column is updated together with sustained_hr_load
// because both come from the same orchestrator run — splitting them
// would require duplicate compute work or stale flags during the
// brief window between two UPDATEs. `flags` always written even when
// the load itself is gated to zero (e.g. stale_stress flag fires
// AND empty load → ([stale_stress], 0)).
func (s *DB) upsertSustainedHRLoadForDate(date string, loc *time.Location) {
	res, ok := s.ComputeSustainedHRLoadForDate(date, loc)
	if !ok {
		return
	}
	flags := res.Flags
	if flags == nil {
		flags = []string{}
	}
	// Sentinel for the SustainedHRLoadZ scalar — only update when
	// finite. Flags update independently of load validity.
	var load *float64
	if isFiniteFloat(res.SustainedHRLoadZ) {
		v := res.SustainedHRLoadZ
		load = &v
	}
	ctx, cancel := queryCtx()
	defer cancel()
	if _, err := s.pool.Exec(ctx, `
		UPDATE daily_scores
		   SET sustained_hr_load = COALESCE($2, sustained_hr_load),
		       stress_flags      = $3
		 WHERE date = $1`, date, load, flags); err != nil {
		log.Printf("upsertSustainedHRLoadForDate %s: %v", date, err)
	}
}

// UpsertSustainedHRLoadForDate is the exported wrapper, mirroring the
// PR-2 UpsertBaselineHROvernightForDate pattern. Used by any future
// one-off cmd that wants to recompute a specific date without rerunning
// the full BackfillAggregates pass.
func (s *DB) UpsertSustainedHRLoadForDate(date string, loc *time.Location) {
	s.upsertSustainedHRLoadForDate(date, loc)
}

// buildSustainedHRLoadAll backfills daily_scores.sustained_hr_load for
// every existing date. Called from BackfillAggregates AFTER the sleep
// block + baseline_hr_overnight pass so the orchestrator's per-channel
// PersonalBaseline reads see the freshest data.
//
// Logs+continues on individual-date errors — same pattern as
// buildBaselineHROvernightAll. One bad night shouldn't fail a
// months-long backfill.
func (s *DB) buildSustainedHRLoadAll(force bool) {
	_ = force
	ctx, cancel := longCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT date FROM daily_scores ORDER BY date`)
	if err != nil {
		log.Printf("sustained_hr_load list: %v", err)
		return
	}
	var dates []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			continue
		}
		dates = append(dates, d)
	}
	if err := rows.Err(); err != nil {
		// Cursor / transport failure mid-iteration. Partial dates
		// slice would silently produce a misleading
		// "filled for N dates" log without the whole history
		// touched. Bail with the error logged instead.
		rows.Close()
		log.Printf("sustained_hr_load iter: %v", err)
		return
	}
	rows.Close()
	if len(dates) == 0 {
		return
	}
	loc := reportTZLocation()
	for _, d := range dates {
		s.upsertSustainedHRLoadForDate(d, loc)
	}
	log.Printf("sustained_hr_load filled for %d dates", len(dates))
}
