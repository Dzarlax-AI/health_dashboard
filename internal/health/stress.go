package health

import "math"

// HourZShift returns the per-hour z-score for one heart-rate hour
// against the rolling personal baseline:
//
//	z = (hourHR − baseline) / sd
//
// Inputs come from storage.HourlyHRSeriesForAwakeWindow (the hour's
// `Median5minMinHR`) and storage.PersonalBaseline(ChannelHRAwake)
// (the 30-day rolling median + MAD-derived SD with the §4.1 SD
// floor already applied).
//
// Returns:
//   - 0 when sd <= 0 (degenerate baseline — caller has already
//     enforced the SD floor, so this only fires on zero-sample
//     state=cold which should have been gated upstream).
//   - 0 when either input is NaN/±Inf (caller treats as "no data
//     for this hour", same as a coverage gap).
//   - The z-score otherwise — positive when this hour is hotter
//     than baseline (potential stress), negative when cooler
//     (resting / parasympathetic dominance).
func HourZShift(hourHR, baseline, sd float64) float64 {
	if !isFinite(hourHR) || !isFinite(baseline) || !isFinite(sd) {
		return 0
	}
	if sd <= 0 {
		return 0
	}
	return (hourHR - baseline) / sd
}

// MinHRCoverageHours is the §4.4 MIN_COVERAGE gate: below this many
// hours of HR-covered awake time, the day's sustained_hr_load is
// unreliable (watch off charger, sync gap) and DrainV2 falls back to
// v2.0 (kcal-only) drain with a stale_stress flag.
//
// Lives here in `internal/health` because both the storage-side
// orchestrator (sustained_hr_load.go) and any future verdict-layer
// consumer should reference the same named constant. 8 hours is the
// spec default and pinned by intent — change requires touching
// STRESS_MEASUREMENT.md § too.
const MinHRCoverageHours = 8

// isFinite is the shared NaN/±Inf guard. Lives in this file
// because energy_v2.go's helpers.go-resident isFiniteFloat is
// package-private to storage; rather than cross-package coupling,
// duplicate a 2-line guard here.
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
