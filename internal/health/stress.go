package health

// Stress v2.2 — pure-math kernel.
//
// This file holds the formulas behind the stratified stress flags
// (§4.3) and the sustained-load drain term (§4.4) from
// STRESS_MEASUREMENT.md. Orchestration (loading per-channel
// baselines, computing per-hour z-series, persisting flags, wiring
// into EnergyBank DrainV2) lands in later PRs. Every symbol below is
// dead code by design until PR-8 — the unit-test suite is the only
// caller.
//
// Design rationale and validation: STRESS_MEASUREMENT.md
// §4.2-§4.4 and §6.

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

// SustainedHRLoad implements the §4.4 drain integral:
//
//	sustained_hr_load[d] = Σ_h max(0, hr_z_hour[h] − zThreshold)
//
// Summed over the per-hour z-series for the awake window. Only hours
// whose z exceeds `zThreshold` (typically 0.5, settings-driven from
// PR-7) contribute, and the contribution scales with how far above
// threshold the hour ran. The result is the "z-load" that feeds the
// β · sustained_hr_load term in DrainV2 (PR-8).
//
// Callers MUST gate this on the §4.4 MIN_COVERAGE = 8 hours
// requirement (counted via storage.HRCoverageHours) — pass an empty
// slice or zero out the result when coverage is insufficient. The
// kernel itself doesn't know about coverage; it just sums what it's
// given.
//
// NaN-tolerant: non-finite z values contribute 0 (same as below-
// threshold). Negative `zThreshold` is degenerate but allowed —
// drives the result up artificially, caller's responsibility.
func SustainedHRLoad(hourZ []float64, zThreshold float64) float64 {
	if !isFinite(zThreshold) {
		return 0
	}
	var total float64
	for _, z := range hourZ {
		if !isFinite(z) {
			continue
		}
		if z > zThreshold {
			total += z - zThreshold
		}
	}
	return total
}

// AcuteStress implements the §4.3 acute_stress flag:
//
//	exists window <2h where hr_z_hour[h] > +2
//
// The "window <2h" wording from the spec resolves to "at least one
// hour with z > +2, regardless of whether it's part of a longer
// run". A short HR spike from a startle / caffeine hit / pre-deploy
// nerves will surface as 1-2 isolated hours of z>+2; if it's longer
// (≥2h), it's no longer "acute" and the sustained-load flag is the
// correct signal.
//
// Per §4.3, this flag drives NO downstream behaviour ("no action —
// transient, no point telling user"). It exists for completeness
// against the spec table and for future briefing-layer use.
//
// NaN-tolerant: non-finite values are skipped (no false positive on
// data gaps).
func AcuteStress(hourZ []float64) bool {
	for _, z := range hourZ {
		if isFinite(z) && z > acuteZThreshold {
			return true
		}
	}
	return false
}

// SustainedLoadFlag implements the §4.3 sustained_load flag:
//
//	hr_z_hour[h] > +1 sustained ≥4h consecutive
//
// Returns true when the input contains at least one run of
// `sustainedMinHours` consecutive hours, all of which have z above
// `sustainedZThreshold` (1.0).
//
// Distinct from `SustainedHRLoad` (the quantitative drain integral)
// — same physiology, different consumers: this flag goes to the
// verdict layer; the integral goes to the drain math. A day can have
// a positive `SustainedHRLoad` while `SustainedLoadFlag` is false
// (one z=1.5 hour adds to drain but isn't a 4-hour run).
//
// NaN-tolerant: non-finite z values break a run (treated as missing
// hours that interrupt the consecutive sequence).
func SustainedLoadFlag(hourZ []float64) bool {
	run := 0
	for _, z := range hourZ {
		if !isFinite(z) {
			run = 0
			continue
		}
		if z > sustainedZThreshold {
			run++
			if run >= sustainedMinHours {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

// IllnessSignature implements the §4.3 illness_signature flag:
//
//	(temp_shift > +1) AND (resp_shift > +1) AND (hrv_drop > +1)
//
// All three channels deviating in the illness direction together.
// `hrvDrop` follows the §4.2 convention `(baseline − today) / sd` so
// "drop > +1" means today's HRV is significantly below baseline (1
// SD or more) — i.e. HRV ↓↓ in the illness pattern.
//
// Per §4.3, this flag does NOT factor into drain — the HR rise that
// flagged illness is already in `sustained_hr_load`, and adding an
// illness multiplier would double-count. Instead it routes to the
// verdict layer: overrides verdict_reason text and suppresses AI
// `push_hard` recommendation.
//
// Requires all three signals — any NaN means "not enough evidence",
// returns false. The HRV channel is the noisiest (event-triggered on
// Apple Watch); the spec accepts that in practice this flag fires
// only when the user has consistent HRV recording.
func IllnessSignature(tempZ, respZ, hrvDrop float64) bool {
	if !isFinite(tempZ) || !isFinite(respZ) || !isFinite(hrvDrop) {
		return false
	}
	return tempZ > illnessZThreshold &&
		respZ > illnessZThreshold &&
		hrvDrop > illnessZThreshold
}

// RecoveryDebt implements the §4.3 recovery_debt flag:
//
//	overnight (hrv_drop > +1) AND (rhr_shift > +0.5)
//
// Both inputs are overnight (not awake-window) — feed
// PersonalBaseline(ChannelHRV) and PersonalBaseline(ChannelHROvernight).
// "hrv_drop > +1" means HRV is meaningfully below baseline; "rhr_shift
// > +0.5" means overnight RHR is above baseline. Together this is
// the classic "yesterday's load caught up to me" overnight signal.
//
// Per §4.3, this flag factors into next-day readiness baseline
// (consumed by PR-9 verdict layer), but does NOT modify the day's
// drain (drain is already complete by EOD; recovery_debt is a
// forward-looking signal).
//
// Asymmetric thresholds (1.0 for HRV vs 0.5 for RHR) come from the
// spec — HRV is the primary recovery signal (higher information per
// reading) and gets the stricter gate; overnight RHR is secondary
// and can sign-flip on vagal-rebound nights (§v2.5 in
// ENERGY_BANK.md), so the half-SD gate keeps it from dragging the
// flag down on noisy nights.
func RecoveryDebt(hrvDrop, rhrShift float64) bool {
	if !isFinite(hrvDrop) || !isFinite(rhrShift) {
		return false
	}
	return hrvDrop > recoveryHRVThreshold &&
		rhrShift > recoveryRHRThreshold
}

// ParasympatheticRebound implements the §4.3 flag:
//
//	(hr_shift > +1) AND (hrv_drop < −1)
//
// "Elevated HR but HRV ABOVE normal" — vagal rebound after sustained
// stress or a heavy training session. Physiologically real: the body
// signals "expended autonomic resource AND switched on the recovery
// machinery". hrvDrop < −1 (i.e. today's HRV is >1 SD ABOVE baseline,
// remember §4.2 convention is `baseline − today`) is the "rebound"
// signature.
//
// Per §4.3, this flag is an **interpretation** signal — it does NOT
// subtract from `sustained_hr_load` and does NOT amplify it. The HR
// rise was real autonomic expenditure and stays in drain; the flag
// enriches `verdict_reason` text ("autonomic state mixed — HR high,
// HRV high, likely recovery phase, not acute stress"). Without this
// flag the day would otherwise classify as sustained_load only,
// missing the recovery-phase nuance.
func ParasympatheticRebound(dayHRShift, hrvDrop float64) bool {
	if !isFinite(dayHRShift) || !isFinite(hrvDrop) {
		return false
	}
	return dayHRShift > reboundHRThreshold &&
		hrvDrop < reboundHRVThreshold
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

// Threshold constants from STRESS_MEASUREMENT.md §4.3. Held as
// named constants so callers and tests reference them by meaning, not
// by magic numbers. v2.5 cohort study may tune these; pinned in
// TestThresholdConstants_LoadBearing so any change is intentional.
//
// All are dimensionless z-score thresholds.
const (
	// acuteZThreshold — single hour above z>+2 triggers acute flag.
	acuteZThreshold = 2.0

	// sustainedZThreshold — per-hour z must exceed this AND continue
	// for sustainedMinHours consecutive hours to trip sustained_load.
	sustainedZThreshold = 1.0
	sustainedMinHours   = 4

	// illnessZThreshold — same +1 threshold applied to temp / resp /
	// hrv-drop simultaneously. Three-channel AND keeps false-positive
	// rate low (any individual channel hitting +1 isn't rare).
	illnessZThreshold = 1.0

	// recoveryHRVThreshold / recoveryRHRThreshold — asymmetric per
	// §4.3 commentary about HRV being the primary signal.
	recoveryHRVThreshold = 1.0
	recoveryRHRThreshold = 0.5

	// reboundHRThreshold / reboundHRVThreshold — note the negative
	// HRV gate: "HRV ABOVE baseline by >1 SD" using the (baseline −
	// today) / sd convention is hrvDrop < −1.
	reboundHRThreshold  = 1.0
	reboundHRVThreshold = -1.0
)

// isFinite is the shared NaN/±Inf guard. Lives in this file
// because energy_v2.go's helpers.go-resident isFiniteFloat is
// package-private to storage; rather than cross-package coupling,
// duplicate a 2-line guard here.
func isFinite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}
