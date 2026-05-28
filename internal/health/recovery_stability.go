// Package health — Recovery Stability sub-score helpers.
//
// Pure functions for the Recovery Stability writer described in
// READINESS_REDESIGN_PLAN.md §4.2. The storage-side writer in
// internal/storage/ consumes ComputeSleepEfficiency to decide whether a
// night is eligible and what the target value is; this file holds the
// data transform with no I/O so it is trivial to test.
//
// Eligibility patterns implemented (plan §4.2.1):
//
//   normal               → sleep_total ∈ [3.95,14], staged > 0, awake > 0
//                          → eff = total / (total + awake)
//   ok_awake_structural_zero
//                        → staged > 0, awake row absent, staged ≈ total
//                          (within tolerance) → eff = 1.0
//   missing_awake_unknown
//                        → staged > 0, awake row absent, but staged
//                          inconsistent with total → ineligible
//   sleep_total_out_of_range
//                        → total NULL, ≤0, or ∉ [3.95,14] → ineligible
//   coarse_only_source   → no staged time (deep+rem+core all NULL/0),
//                          unspecified may be present → ineligible for
//                          efficiency primary target
//
// Source-fingerprint check from the plan ("source matches a known
// Apple Watch fingerprint") is intentionally skipped in Phase 0. The
// upstream sleep cross-validation in storage.aggregates already picks
// a single source per night before data reaches `daily_scores`; by
// then the source identity is no longer surfaced. The staged-vs-total
// consistency check carries the load — coarse-only sources have
// `staged = 0` and short-circuit to `coarse_only_source` regardless.

package health

import "math"

// SleepEligibilityReason mirrors storage.Eligibility* string consts.
// Defined here rather than imported because internal/health must not
// depend on internal/storage (storage imports health, not the other
// way around).
const (
	SleepEligibilityOK                    = "ok"
	SleepEligibilityOKAwakeStructuralZero = "ok_awake_structural_zero"
	SleepEligibilityMissingAwakeUnknown   = "missing_awake_unknown"
	SleepEligibilitySleepTotalOutOfRange  = "sleep_total_out_of_range"
	SleepEligibilityCoarseOnlySource      = "coarse_only_source"
	// SleepEligibilityDataMissing distinguishes nights where the sleep
	// row is entirely absent from the source (no metric_points entries
	// for `sleep_total`) from physiologically out-of-range short or
	// long nights. Both are ineligible, but their operational meaning
	// differs: data_missing → device off / sync gap / pre-sync period;
	// out_of_range → real but unusable short sleep (nap, all-nighter).
	// Introduced in formula_version 2.
	SleepEligibilityDataMissing = "sleep_data_missing"
)

// Sleep capture classes describe whether the sleep source appears usable
// as evidence. They deliberately do not change Recovery target eligibility;
// storage writes them next to targets/features as diagnostics.
const (
	SleepCaptureGood                  = "good_capture"
	SleepCapturePartialShort          = "partial_capture_short"
	SleepCaptureMissing               = "missing_capture"
	SleepCaptureCoarseOnly            = "coarse_only_capture"
	SleepCaptureStageMismatch         = "stage_mismatch_capture"
	SleepCaptureInvalidTotal          = "invalid_total_capture"
	SleepCaptureRealShortCandidate    = "real_short_sleep_candidate"
	SleepCaptureLateSleepPending      = "late_sleep_pending"
	SleepCaptureReasonOK              = "ok"
	SleepCaptureReasonNoTotal         = "no_sleep_total"
	SleepCaptureReasonShortWithStages = "short_total_with_stages"
	SleepCaptureReasonShortCoarseOnly = "short_total_coarse_only"
	SleepCaptureReasonNoStages        = "no_staged_sleep"
	SleepCaptureReasonStageMismatch   = "stage_total_mismatch"
	SleepCaptureReasonInvalidTotal    = "invalid_sleep_total"
)

// Sleep eligibility thresholds. Documented in plan §4.2.
const (
	// sleepTotalNominalMinHours is the methodology cutoff. The
	// eligibility code applies sleepTotalMinHours below as a narrow
	// numerical tolerance around this hard threshold; 3.99h should not
	// be treated differently from 4.000h solely because of source
	// rounding, while clearly short nights remain ineligible.
	sleepTotalNominalMinHours = 4.0

	// sleepTotalMinHours / sleepTotalMaxHours bound the physiologically
	// plausible nightly sleep range used for eligibility (narrower than
	// the [0,14] range in quality.go because efficiency on extreme
	// outliers is meaningless).
	sleepTotalMinHours = 3.95
	sleepTotalMaxHours = 14.0

	// structuralZeroTolerance is the relative gap allowed between
	// sleep_total and the sum of staged components when promoting a
	// missing-awake night to `ok_awake_structural_zero`. Set to 2%
	// (plan §4.2.1) to match the existing sleep cross-validation
	// tolerance for source comparison.
	structuralZeroTolerance = 0.02
)

// SleepRow is the nullable representation of a daily_scores row's
// sleep columns. Each *float64 is nil when the column is SQL NULL.
//
// Conventionally: sleep_total is the source's reported asleep duration
// (NOT time-in-bed); sleep_awake is awake time within the sleep period;
// deep/rem/core are staged components that should sum to roughly
// sleep_total when the source emits real staging; unspecified is the
// catch-all from coarse-only sources (RingConn / iPhone Sleep Schedule)
// that report asleep duration without stage breakdown.
type SleepRow struct {
	Date        string
	Total       *float64
	Deep        *float64
	REM         *float64
	Core        *float64
	Awake       *float64
	Unspecified *float64
}

// SleepEfficiencyResult is the eligibility verdict + efficiency value
// for a single night.
type SleepEfficiencyResult struct {
	Eligible          bool
	EligibilityReason string
	Efficiency        *float64 // nil when ineligible
}

type SleepCaptureConfidenceResult struct {
	Class         string
	Confidence    float64
	Reason        string
	LowConfidence bool
}

// ComputeSleepEfficiency applies the plan §4.2.1 eligibility decision
// tree to one SleepRow and returns the verdict. Pure function — no
// state, no I/O. Tested directly in recovery_stability_test.go.
func ComputeSleepEfficiency(r SleepRow) SleepEfficiencyResult {
	// Absent sleep_total row = no data was recorded for the night.
	// Distinct from a present-but-unusable value (handled below) — the
	// former usually means device off / sync gap / pre-tracking period,
	// the latter means real sleep that was too short or too long for
	// efficiency to be meaningful.
	if r.Total == nil {
		return SleepEfficiencyResult{
			Eligible:          false,
			EligibilityReason: SleepEligibilityDataMissing,
		}
	}
	total := *r.Total
	if total <= 0 || total < sleepTotalMinHours || total > sleepTotalMaxHours {
		return SleepEfficiencyResult{
			Eligible:          false,
			EligibilityReason: SleepEligibilitySleepTotalOutOfRange,
		}
	}

	deep := safeFloat(r.Deep)
	rem := safeFloat(r.REM)
	core := safeFloat(r.Core)
	staged := deep + rem + core
	unspecified := safeFloat(r.Unspecified)

	// No staged time and no unspecified either is treated as
	// coarse_only_source. The night was recorded by the source but
	// without any breakdown we can read; not useful for an efficiency
	// primary target. (Strictly speaking this could be a malformed
	// row; the downstream feature_snapshot will still carry the raw
	// fields for diagnostics.)
	if staged <= 0 {
		return SleepEfficiencyResult{
			Eligible:          false,
			EligibilityReason: SleepEligibilityCoarseOnlySource,
		}
	}

	// awake row absent (or explicit zero — defensive, see CLAUDE.md
	// note on the upstream qty > 0 filter).
	if r.Awake == nil || *r.Awake == 0 {
		// Promote to structural zero only when staged ≈ total. The
		// rationale: when a source emits full staging that adds up to
		// its claimed total, the absence of the awake row almost
		// always reflects "no waking detected" rather than "data
		// missing for an unknown reason".
		rel := math.Abs(staged-total) / total
		// 1e-9 absorbs floating-point error so a source reporting
		// staged exactly 102.000…% of total still resolves cleanly to
		// structural zero. The semantic boundary is 2%; the epsilon
		// is purely arithmetic.
		if rel <= structuralZeroTolerance+1e-9 {
			eff := 1.0
			return SleepEfficiencyResult{
				Eligible:          true,
				EligibilityReason: SleepEligibilityOKAwakeStructuralZero,
				Efficiency:        &eff,
			}
		}
		return SleepEfficiencyResult{
			Eligible:          false,
			EligibilityReason: SleepEligibilityMissingAwakeUnknown,
		}
	}

	awake := *r.Awake
	// efficiency = asleep / time_in_bed = total / (total + awake).
	// sleep_total here is the source's reported asleep duration
	// (Apple Watch convention), so adding awake gives time in bed.
	timeInBed := total + awake
	if timeInBed <= 0 {
		// Cannot happen given total > sleepTotalMinHours and awake >= 0,
		// but guard against future changes that loosen earlier branches.
		return SleepEfficiencyResult{
			Eligible:          false,
			EligibilityReason: SleepEligibilityMissingAwakeUnknown,
		}
	}
	eff := total / timeInBed
	// staged is computed but not used past the coarse-source branch.
	// Reference it explicitly so future readers see it has been
	// considered — and so static analysis does not flag a dead read.
	_ = unspecified
	_ = staged
	return SleepEfficiencyResult{
		Eligible:          true,
		EligibilityReason: SleepEligibilityOK,
		Efficiency:        &eff,
	}
}

func ComputeSleepCaptureConfidence(r SleepRow) SleepCaptureConfidenceResult {
	if r.Total == nil {
		return SleepCaptureConfidenceResult{
			Class:         SleepCaptureMissing,
			Confidence:    0,
			Reason:        SleepCaptureReasonNoTotal,
			LowConfidence: true,
		}
	}
	total := *r.Total
	if total <= 0 || total > sleepTotalMaxHours {
		return SleepCaptureConfidenceResult{
			Class:         SleepCaptureInvalidTotal,
			Confidence:    0.05,
			Reason:        SleepCaptureReasonInvalidTotal,
			LowConfidence: true,
		}
	}

	deep := safeFloat(r.Deep)
	rem := safeFloat(r.REM)
	core := safeFloat(r.Core)
	staged := deep + rem + core
	unspecified := safeFloat(r.Unspecified)
	hasStaged := staged > 0

	if total < sleepTotalMinHours {
		if hasStaged {
			return SleepCaptureConfidenceResult{
				Class:         SleepCapturePartialShort,
				Confidence:    0.35,
				Reason:        SleepCaptureReasonShortWithStages,
				LowConfidence: true,
			}
		}
		if unspecified > 0 {
			return SleepCaptureConfidenceResult{
				Class:         SleepCaptureCoarseOnly,
				Confidence:    0.25,
				Reason:        SleepCaptureReasonShortCoarseOnly,
				LowConfidence: true,
			}
		}
		return SleepCaptureConfidenceResult{
			Class:         SleepCaptureRealShortCandidate,
			Confidence:    0.20,
			Reason:        SleepCaptureReasonInvalidTotal,
			LowConfidence: true,
		}
	}

	if !hasStaged {
		if unspecified > 0 {
			return SleepCaptureConfidenceResult{
				Class:         SleepCaptureCoarseOnly,
				Confidence:    0.55,
				Reason:        SleepCaptureReasonNoStages,
				LowConfidence: true,
			}
		}
		return SleepCaptureConfidenceResult{
			Class:         SleepCaptureMissing,
			Confidence:    0.15,
			Reason:        SleepCaptureReasonNoStages,
			LowConfidence: true,
		}
	}

	rel := math.Abs(staged-total) / total
	if rel > 0.25 {
		return SleepCaptureConfidenceResult{
			Class:         SleepCaptureStageMismatch,
			Confidence:    0.50,
			Reason:        SleepCaptureReasonStageMismatch,
			LowConfidence: true,
		}
	}
	return SleepCaptureConfidenceResult{
		Class:         SleepCaptureGood,
		Confidence:    0.95,
		Reason:        SleepCaptureReasonOK,
		LowConfidence: false,
	}
}

func safeFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
