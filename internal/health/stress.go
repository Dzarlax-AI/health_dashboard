package health

import "math"

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
