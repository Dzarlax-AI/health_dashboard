package health

// Stress v2.2 §4.5 validation rubric — pure-math kernel.
//
// Answers the operational question: "may we flip
// settings.energy.stress_drain_enabled for this tenant?"
//
// The rubric runs against the tenant's own history (no self-report,
// no external integrations) and produces a verdict from
// {validated, weak, inconclusive, wrong_direction}. Storage-side
// orchestration in internal/storage/stress_validation.go fetches the
// per-day series; the kernel only operates on float slices and the
// derived correlation coefficients.
//
// Channel ranking from §4.5:
//   1. Next-morning HRV residual (primary)    Pearson(load, HRV[d+1])
//   2. Next-morning RHR shift (secondary)     Pearson(load, RHR_shift[d+1])
//   3. Sleep architecture degradation         sign-agreement vote
//   4. Test-retest stability                  CI-pinned via TestBankConvergence
//
// Channel 4 is delegated to the existing convergence test in CI
// (per spec §4.5) so this kernel only consumes channels 1-3.

import (
	"fmt"
	"math"
)

// ValidationChannel carries one channel's contribution to the
// rubric. `R` is the Pearson coefficient (nil = "cold" / skipped),
// `N` is the sample size used, `Sparse` flags the sparsity preflight
// (e.g. <15 overnight HRV samples per §4.5).
type ValidationChannel struct {
	R      *float64 `json:"r,omitempty"`
	N      int      `json:"n"`
	Sparse bool     `json:"sparse,omitempty"`
}

// SleepChannel is channel 3's structured output. Three sub-signals
// vote on sign agreement; aggregate is the count of sub-signals
// pointing in the expected "stress → worse sleep" direction. Each
// sub-signal contributes only when its sample size and Pearson
// magnitude clear the per-signal floor.
type SleepChannel struct {
	OnsetLatencyR *float64 `json:"onset_latency_r,omitempty"`
	AwakeR        *float64 `json:"awake_r,omitempty"`
	DeepPctR      *float64 `json:"deep_pct_first_third_r,omitempty"`

	// AgreementVotes is the number of sub-signals in the
	// physiologically-expected direction (latency↑, awake↑, deep↓).
	// Range 0..3; rubric counts this as "channel 3 agrees" when ≥2.
	AgreementVotes int `json:"agreement_votes"`
	N              int `json:"n"`
}

// ValidationReport is the full rubric output. JSON-stable for the
// /api/admin/stress-validation endpoint.
type ValidationReport struct {
	WindowDays int `json:"window_days"`
	Days       int `json:"days"` // dates with non-null sustained_hr_load

	Channel1HRV   ValidationChannel `json:"channel_1_hrv"`
	Channel2RHR   ValidationChannel `json:"channel_2_rhr"`
	Channel3Sleep SleepChannel      `json:"channel_3_sleep"`

	// Verdict is one of {validated, weak, inconclusive,
	// wrong_direction}. See RubricDecide for the decision table.
	Verdict string `json:"verdict"`

	// Reason is a one-sentence human-readable explanation of the
	// verdict — surfaces in /admin UI + audit logs.
	Reason string `json:"reason"`

	// Flags is a subset of {calibration_weak, data_quality_warn} —
	// surface alongside the verdict so the briefing layer can
	// soften narrative when the validation is fragile.
	Flags []string `json:"flags,omitempty"`
}

// PearsonR returns the Pearson product-moment correlation between
// `xs` and `ys` plus the effective sample count. Pairs containing
// NaN/±Inf in either coordinate are skipped (paired). When n < 3 or
// either coordinate's variance is zero, returns (0, n, false).
//
// Pure function — pinned by unit test against a worked example.
func PearsonR(xs, ys []float64) (r float64, n int, ok bool) {
	if len(xs) != len(ys) {
		return 0, 0, false
	}
	var sumX, sumY, sumXX, sumYY, sumXY float64
	for i, x := range xs {
		y := ys[i]
		if !isFinite(x) || !isFinite(y) {
			continue
		}
		sumX += x
		sumY += y
		sumXX += x * x
		sumYY += y * y
		sumXY += x * y
		n++
	}
	if n < 3 {
		return 0, n, false
	}
	fn := float64(n)
	meanX := sumX / fn
	meanY := sumY / fn
	varX := sumXX/fn - meanX*meanX
	varY := sumYY/fn - meanY*meanY
	if varX <= 0 || varY <= 0 {
		return 0, n, false
	}
	cov := sumXY/fn - meanX*meanY
	denom := math.Sqrt(varX * varY)
	return cov / denom, n, true
}

// Thresholds from STRESS_MEASUREMENT.md §4.5 decision rubric.
// Pinned as named constants so changes are intentional and reviewed.
const (
	// HRVChannelMinSamples — sparsity preflight per §4.5. Below
	// this the channel is "cold" and the rubric falls back to
	// channels 2+3 with a 2-of-3 agreement requirement.
	HRVChannelMinSamples = 15

	// ValidatedHRVThreshold — Pearson r ≤ −0.3 on channel 1 with
	// at least one cross-check agreeing in sign → validated.
	ValidatedHRVThreshold = -0.3

	// WeakHRVThreshold — Pearson r between −0.3 and −0.1 on
	// channel 1 with ≥2 cross-channel sign agreement → weak.
	WeakHRVThreshold = -0.1

	// InconclusiveHRVThreshold — |r| < 0.1 on channel 1 →
	// inconclusive (data sparsity or low-variance lifestyle).
	InconclusiveHRVThreshold = 0.1

	// SleepAgreementThreshold — channel 3 votes ≥ this count
	// indicate "channel 3 agrees with the stress signal".
	SleepAgreementThreshold = 2
)

// RubricDecide applies the §4.5 decision table to the channel
// outputs and fills in `Verdict` + `Reason` + `Flags` on `r`.
// Pure function — no DB / no I/O.
//
// Decision order matches the spec table:
//
//   r > 0 on channel 1                        → wrong_direction
//   |r| < 0.1 on channel 1                    → inconclusive
//   r ≤ −0.3 with at least one cross-agree    → validated
//   −0.3 < r < −0.1 with ≥2 cross-agree       → weak
//   default (no cross-agreement)              → weak with flag
//
// Disagreement override: even at r ≤ −0.3, if BOTH channels 2 and 3
// disagree in sign, downgrade to weak (per §4.5 commentary).
//
// HRV sparsity preflight: when channel 1 is sparse (cold), fall
// back to a 2-of-3 vote across channels 2 + 3 + (its own sparse
// indicator). If channels 2 and 3 are also cold, mark inconclusive.
func RubricDecide(r *ValidationReport) {
	c1 := r.Channel1HRV
	c2 := r.Channel2RHR
	c3 := r.Channel3Sleep

	// Sparsity preflight — channel 1 must have enough data to
	// produce a meaningful coefficient.
	if c1.Sparse || c1.R == nil {
		decideWithoutHRV(r)
		return
	}

	rHRV := *c1.R

	// Wrong-direction: HRV residual sign is positive (high stress
	// load → HIGHER next-morning HRV is nonsensical for the formula's
	// premise). Suppresses β regardless of other channels.
	if rHRV > 0 {
		r.Verdict = "wrong_direction"
		r.Reason = fmt.Sprintf(
			"Channel 1 HRV correlation r=%+.2f points the wrong way — formula isn't capturing this user's physiology.",
			rHRV,
		)
		return
	}

	// Inconclusive: too weak to call.
	if math.Abs(rHRV) < InconclusiveHRVThreshold {
		r.Verdict = "inconclusive"
		r.Reason = fmt.Sprintf(
			"Channel 1 HRV correlation |r|=%.2f below the 0.10 floor — likely data sparsity or low-variance lifestyle.",
			math.Abs(rHRV),
		)
		r.Flags = append(r.Flags, "data_quality_warn")
		return
	}

	// Cross-channel sign agreement count — RHR shift expected to
	// be POSITIVE (high load → higher next-morning RHR); sleep
	// channel votes count when ≥ SleepAgreementThreshold.
	rhrAgrees := c2.R != nil && *c2.R > 0
	sleepAgrees := c3.AgreementVotes >= SleepAgreementThreshold
	agreeCount := boolToInt(rhrAgrees) + boolToInt(sleepAgrees)

	// Validated path — strong HRV signal AND at least one
	// cross-check pointing the same way.
	if rHRV <= ValidatedHRVThreshold {
		// Disagreement override: BOTH cross-checks must disagree
		// to downgrade. The presence of a cross-check that's
		// uncomputable (nil R) counts as neither agree nor
		// disagree — fall back on whatever else is present.
		bothDisagree := !rhrAgrees && !sleepAgrees && c2.R != nil
		if agreeCount >= 1 && !bothDisagree {
			r.Verdict = "validated"
			r.Reason = fmt.Sprintf(
				"Channel 1 HRV r=%+.2f and %d cross-channel(s) agree — β may be tuned per §6 Q3.",
				rHRV, agreeCount,
			)
			return
		}
		// HRV strong but cross-checks contradict → weak.
		r.Verdict = "weak"
		r.Reason = fmt.Sprintf(
			"Channel 1 HRV r=%+.2f is strong but cross-channels contradict — likely artefact or sparsity.",
			rHRV,
		)
		r.Flags = append(r.Flags, "calibration_weak")
		return
	}

	// Weak band: -0.3 < r < -0.1. Need 2 cross-channels agreeing.
	if agreeCount >= 2 {
		r.Verdict = "weak"
		r.Reason = fmt.Sprintf(
			"Channel 1 HRV r=%+.2f is moderate; both cross-channels agree. β stays at placeholder, recheck weekly.",
			rHRV,
		)
		r.Flags = append(r.Flags, "calibration_weak")
		return
	}
	r.Verdict = "weak"
	r.Reason = fmt.Sprintf(
		"Channel 1 HRV r=%+.2f is moderate but cross-channels don't agree enough — β stays at 0.",
		rHRV,
	)
	r.Flags = append(r.Flags, "calibration_weak")
}

// decideWithoutHRV is the §4.5 sparse-channel-1 fallback. Falls back
// to channels 2+3 with a 2-of-3 agreement requirement. If channels
// 2 and 3 are also cold, returns inconclusive.
func decideWithoutHRV(r *ValidationReport) {
	c2 := r.Channel2RHR
	c3 := r.Channel3Sleep

	if c2.R == nil && c3.N == 0 {
		r.Verdict = "inconclusive"
		r.Reason = "Channel 1 HRV sparse (<15 samples) and channels 2+3 have no data — insufficient evidence."
		r.Flags = append(r.Flags, "data_quality_warn")
		return
	}

	// 2-of-3 across (rhr_positive, sleep_votes_high, sleep_votes_strong).
	// Use sleep's vote count as both a binary "agrees" signal and
	// a "strongly agrees" signal — 2 votes = agrees, 3 = strong.
	rhrAgrees := c2.R != nil && *c2.R > 0
	sleepWeak := c3.AgreementVotes >= SleepAgreementThreshold
	sleepStrong := c3.AgreementVotes >= 3
	agreeCount := boolToInt(rhrAgrees) + boolToInt(sleepWeak) + boolToInt(sleepStrong)

	if agreeCount >= 2 {
		r.Verdict = "weak"
		r.Reason = "Channel 1 HRV sparse; channels 2+3 agree under fallback rubric — β stays at placeholder, recheck weekly."
		r.Flags = append(r.Flags, "calibration_weak", "data_quality_warn")
		return
	}

	r.Verdict = "inconclusive"
	r.Reason = "Channel 1 HRV sparse and fallback channels 2+3 don't form a 2-of-3 agreement — β stays at 0."
	r.Flags = append(r.Flags, "data_quality_warn")
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
