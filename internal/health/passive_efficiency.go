// Package health — Passive Efficiency sub-score helpers.
//
// Pure functions for the Passive Efficiency writer (plan §4.2).
// Target metric is Apple-computed `walking_heart_rate_average`: the
// daily average HR observed during walking segments. Operationalised
// as "how expensive is ordinary movement for the heart today" — a
// universal daily signal that exists even on days without structured
// workouts.
//
// Eligibility is intentionally narrow:
//   ok                       → value present and ∈ [50, 180] bpm
//   no_walking_hr            → no value for the day
//   walking_hr_out_of_range  → value present but outside the physiological band
//
// `no_walking_hr` and `walking_hr_out_of_range` are kept distinct
// because they carry different operational meaning: the first is "we
// didn't see the user walk today" (device off, sedentary day, sync
// gap); the second is "we got a number but it's physiologically
// implausible" (sensor artifact, stress test, mis-classification).

package health

const (
	PassiveEfficiencyOK                  = "ok"
	PassiveEfficiencyNoWalkingHR         = "no_walking_hr"
	PassiveEfficiencyWalkingHROutOfRange = "walking_hr_out_of_range"
)

// Walking HR plausibility band. Set with comfortable margin: 50 bpm
// covers very fit walkers, 180 bpm covers sprint-tempo "walks" that
// are really jogs but still classified by the OS as walking. Anything
// outside is treated as artifact, not as a valid low/high day.
const (
	walkingHRMinBPM = 50.0
	walkingHRMaxBPM = 180.0
)

// WalkingHRRow is the per-date input to the eligibility check. Value
// is the daily mean of `walking_heart_rate_average` over `quality='ok'`
// metric_points rows; nil when no rows exist for the date.
type WalkingHRRow struct {
	Date  string
	Value *float64
}

// WalkingHREligibilityResult mirrors the SleepEfficiencyResult shape
// from recovery_stability.go: eligibility verdict plus the value the
// writer should record as target_value when eligible.
type WalkingHREligibilityResult struct {
	Eligible          bool
	EligibilityReason string
	Value             *float64 // nil when ineligible
}

// ComputeWalkingHREligibility classifies a WalkingHRRow per the rules
// above. Pure function — no state, no I/O.
func ComputeWalkingHREligibility(r WalkingHRRow) WalkingHREligibilityResult {
	if r.Value == nil {
		return WalkingHREligibilityResult{
			Eligible:          false,
			EligibilityReason: PassiveEfficiencyNoWalkingHR,
		}
	}
	v := *r.Value
	if v < walkingHRMinBPM || v > walkingHRMaxBPM {
		return WalkingHREligibilityResult{
			Eligible:          false,
			EligibilityReason: PassiveEfficiencyWalkingHROutOfRange,
		}
	}
	return WalkingHREligibilityResult{
		Eligible:          true,
		EligibilityReason: PassiveEfficiencyOK,
		Value:             r.Value,
	}
}
