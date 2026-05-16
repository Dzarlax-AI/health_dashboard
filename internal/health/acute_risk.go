// Package health — Acute Risk sub-score helpers.
//
// Pure types and constants for the Acute Risk writer in
// internal/storage/acute_risk_writer.go. Acute Risk is a
// classification sub-score (not a value regressand): for row dated
// `t`, target is whether any day in `t+1..t+3` shows an autonomic
// spike — HRV drop ≥ 1.5σ below personal baseline OR RHR rise ≥ 1.5σ
// above baseline. The strict variant requires both same-day on the
// same day inside the window.
//
// Baseline math lives in the writer (windowStatsBefore + per-day
// re-evaluation) because event labelling must use only history
// strictly prior to each candidate day. Including the candidate's own
// value in its own baseline would silently leak target into label.
// This file holds the contract types and the threshold/warmup
// constants — anything that does not need DB or window machinery.
//
// Plan ref: READINESS_REDESIGN_PLAN.md §4.2, §4.2 row "Acute Risk".

package health

// AutonomicRow is the daily HRV/RHR pair pulled from daily_scores.
// Each *float64 is nil when the corresponding column is SQL NULL.
type AutonomicRow struct {
	Date string
	HRV  *float64 // hrv_avg, ms
	RHR  *float64 // rhr_avg, bpm
}

// Eligibility reasons for the Acute Risk writer. Two reasons in Phase 0:
//
//   ok                — paired warmup met (≥ AcuteRiskWarmupMinPaired
//                       paired observations within source_epoch before
//                       day `t+1`).
//   baseline_warmup   — fewer than the minimum paired observations
//                       inside the current source_epoch; baselines for
//                       the t+1..t+3 window cannot be trusted.
//
// A single combined reason covers HRV-sparse and RHR-sparse together
// because the writer's gate is on paired availability — splitting into
// per-channel reasons does not change the eligibility outcome.
const (
	AcuteRiskEligibilityOK              = "ok"
	AcuteRiskEligibilityBaselineWarmup  = "baseline_warmup"
)

// AcuteRiskHRVZThreshold — z-score below which a day counts as an HRV
// drop. Negative because HRV drops are the bad direction (lower HRV =
// elevated autonomic load).
const AcuteRiskHRVZThreshold = -1.5

// AcuteRiskRHRZThreshold — z-score above which a day counts as an RHR
// spike. Positive because RHR rises are the bad direction.
const AcuteRiskRHRZThreshold = 1.5

// AcuteRiskWarmupMinPaired is the minimum number of days in the
// current source_epoch (strictly before `t+1`) with BOTH HRV and RHR
// present required to release the writer from baseline_warmup state.
// 30 paired days gives a sample large enough for a 1.5σ threshold to
// have ~30 effective degrees of freedom (matches the 30-day rolling
// baseline used elsewhere in scoring.go).
const AcuteRiskWarmupMinPaired = 30

// AcuteRiskBaselineWindowDays — trailing window used to compute the
// per-day mean and SD for each candidate day in `t+1..t+3`. 45 days
// matches the adaptive EWMA window pinned in plan §3.3.
const AcuteRiskBaselineWindowDays = 45
