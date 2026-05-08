package health

// EnergyBank v2 — pure-math kernel.
//
// This file holds the formulas only; orchestration (14-day forward
// iteration, snapshot persistence, missing-data imputation) lands in
// later PRs. v1 (computeEnergyBank, scoreEnergyBank) stays in use and
// untouched until PR8 flips the dashboard over. Until then, every
// symbol below is dead code by design — the unit-test suite is the only
// caller.
//
// Design rationale and validation: ENERGY_BANK.md.

import "math"

// SleepQuality maps a single night's sleep stages to a [0, 1] score
// used by the asymptotic restore step. All inputs are in hours.
//
//	duration_factor   = clamp01(total / 8)
//	efficiency_factor = total / (total + awake)        // 1 if no fragmentation
//	structure_factor  = max(0.5, 1
//	                          − max(0, 0.15 − deep/total)
//	                          − max(0, 0.20 − rem/total))
//	sleep_quality     = duration · efficiency · structure
//
// `structure_factor` only penalises (clamped to [0.5, 1.0]) — never
// rewards above 1. The asymptote already provides the upper bound;
// bonus weights would just chase a ceiling that's already there. A
// night with apnea-proxy deep ratio (≈8%) lands at ~0.93 structure;
// a night with no deep at all bottoms out at 0.85; only severely
// fragmented or short nights can push the product low enough to leave
// the bank meaningfully drained.
//
// Edge cases:
//   - total ≤ 0 (data gap): returns 0. Caller is expected to substitute
//     a 7-day trailing average via the imputation path (PR4); falling
//     through with sq=0 here would integrate into a runaway negative
//     bank during gaps, which the imputation step exists to prevent.
//   - awake > total (sensor glitch): efficiency clamps via the
//     definitional formula; we don't special-case it because the input
//     is nonsensical and any choice is wrong. Caller filters via the
//     existing quality.go guards before getting here.
func SleepQuality(totalH, deepH, remH, awakeH float64) float64 {
	if totalH <= 0 {
		return 0
	}
	durationFactor := clamp01(totalH / 8.0)
	efficiencyFactor := totalH / (totalH + awakeH)
	deepPct := deepH / totalH
	remPct := remH / totalH
	deepShortfall := math.Max(0, 0.15-deepPct)
	remShortfall := math.Max(0, 0.20-remPct)
	structureFactor := math.Max(0.5, 1.0-deepShortfall-remShortfall)
	return durationFactor * efficiencyFactor * structureFactor
}

// DrainV2 computes the day's energy drain. v2.0 ships with the
// calorie-only term active (alpha · active_kcal); the β · max(0, HR −
// RHR) · duration term is reserved for v2.2 once HR-per-hour reads are
// wired in. Keeping the signature stable now means later PRs add
// inputs without breaking callers.
//
// alpha is the personalised drain coefficient (EnergyConfig.EffectiveAlpha
// in the storage layer). For v2.0 launch the default is 0.08 — see
// ENERGY_BANK.md § Validation for the empirical derivation on 31 days
// of historical data.
//
// Both inputs are floored at 0. Negative kcal would come from sensor
// glitches; negative alpha would come from a calibrator bug or a manual
// settings override gone wrong. Either would invert the formula's
// semantics — drain becomes credit, "you exercised hard" becomes "have
// some free energy" — so we refuse to integrate either sign and just
// return 0.
func DrainV2(activeKcal float64, alpha float64) float64 {
	if activeKcal < 0 {
		activeKcal = 0
	}
	if alpha < 0 {
		alpha = 0
	}
	return alpha * activeKcal
}

// AsymptoticCapacity is the restore step: yesterday's residual bank
// approaches 100 by a fraction equal to last night's sleep quality.
//
//	capacity = bank_yesterday + (100 − bank_yesterday) · sleep_quality
//
// Three properties this gives us that an additive restore did not:
//
//  1. Multi-night sleep deficit accumulates. A poor night drags the
//     anchor down; the next morning starts from a lower ceiling, so
//     two poor nights in a row depress capacity progressively.
//  2. Capacity never pins at 100 unless sleep_quality = 1 AND the
//     bank already starts at 100 — which is mathematically vacuous.
//     A real distribution gains a meaningful lower tail.
//  3. Cross-day carryover comes for free: with sq=0 (data gap or zero
//     sleep) the formula returns bank_yesterday unchanged, so the
//     bank doesn't reset on day boundaries.
//
// `bankYesterday` may be negative (the bank is signed in DB to preserve
// the "in the hole" signal for AI). When it's strongly negative and
// sleep_quality is high, the formula still cannot exceed 100 — the
// (100 − bank) gap shrinks faster than sq pushes us up. Verified
// numerically: bank=−50, sq=1.0 → capacity = 100. Bank=−50, sq=0.5 →
// capacity = 25. Bank=100, sq=1.0 → capacity = 100 (no overshoot).
func AsymptoticCapacity(bankYesterday, sleepQuality float64) float64 {
	return bankYesterday + (100.0-bankYesterday)*sleepQuality
}

// ClampSignedBank enforces the [-50, 100] window on the signed bank
// stored in the DB. Above 100 the formula is mathematically incapable
// of overshooting (see AsymptoticCapacity proof above), so the upper
// clamp catches only floating-point drift; below -50 the signal
// saturates and further integration is noise.
//
// The API/UI layer applies a separate [0, 100] clamp before display —
// users shouldn't see scary minus numbers — but the AI prompt path
// reads the signed value directly so it can frame a sustained deficit
// as "you're in the hole".
func ClampSignedBank(bank float64) float64 {
	if bank < -50 {
		return -50
	}
	if bank > 100 {
		return 100
	}
	return bank
}

