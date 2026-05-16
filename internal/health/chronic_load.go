// Package health — Chronic Load sub-score helpers.
//
// Plan §4.2 row. Phase 0 emits labels + features only — no verdict
// logic, no UI consumer. Two parallel target_kinds with distinct
// semantics:
//
//   chronic_label          — primary: sustained deterioration in
//                            Recovery Stability. 3d-rolling
//                            sleep_efficiency drops below source-epoch
//                            EWMA45 baseline by >1σ on ≥5 days inside
//                            the forward window t+1..t+14.
//
//   chronic_acute_density  — secondary: ≥3 Acute Risk OR-events
//                            (event_t1_t3 = 1, eligible) inside the
//                            same forward window. Counts the *primary*
//                            Acute Risk label, not strict — strict is
//                            too rare (~2% base rate) to make a
//                            meaningful density signal.
//
// Methodological invariants enforced in the writer:
//
//   - Per-candidate baselines for the primary label. For each
//     candidate day `d ∈ t+1..t+14`, the EWMA45 mean/SD against which
//     `rolling_3d(d)` is compared must come from Recovery values for
//     dates strictly before `d` (and clipped to the current
//     source_epoch). A baseline frozen at row `t` would let the
//     candidate's own deterioration shift the threshold on later days.
//
//   - Forward window reads target_snapshots rows for sub_scores
//     Recovery (rolling_3d) and Acute Risk (event_t1_t3). The writer
//     does not recompute those — it consumes their output. Chronic
//     Load must be run AFTER Recovery and Acute Risk are backfilled
//     for the same range.

package health

// Eligibility reasons. Phase 0 surface is small:
//   ok                — paired warmup met (≥ ChronicLoadWarmupMinPaired
//                       eligible Recovery rolling_3d rows inside the
//                       current source_epoch, strictly before t+1).
//   baseline_warmup   — fewer than the minimum eligible Recovery
//                       observations to set a 45-day EWMA threshold.
const (
	ChronicLoadEligibilityOK             = "ok"
	ChronicLoadEligibilityBaselineWarmup = "baseline_warmup"
)

// ChronicLoadDeteriorationZThreshold — Recovery rolling_3d at day `d`
// counts as deteriorated when it falls below its prior EWMA45 mean by
// more than this many sample SDs. Per plan §4.2: ">1σ" → threshold = -1.0σ.
// Compared as strict `<` so a value exactly at `mean − 1σ` does NOT
// trigger; the breach requires *more* than 1σ below.
const ChronicLoadDeteriorationZThreshold = -1.0

// ChronicLoadMinBreachDays — primary label fires when the forward
// window contains at least this many deteriorating days.
const ChronicLoadMinBreachDays = 5

// ChronicLoadForwardWindowDays — the forward window length used by
// both target_kinds (chronic_label and chronic_acute_density). 14
// days matches plan §4.2; chosen so a model trained on this label
// captures the slow-burn regime that the daily-3d window of Acute
// Risk does not see.
const ChronicLoadForwardWindowDays = 14

// ChronicLoadMinAcuteDensity — secondary label fires when the count
// of Acute Risk OR-events (eligible event_t1_t3 = 1) inside the
// forward window is at least this many.
//
// Calibration threshold, NOT a physiological constant.
//
// `7` was chosen empirically from the Phase 1 floor distribution on
// the production test slice (`source_2025_current`, 2025-01-01 →
// 2026-05-15). The Acute OR base rate of ~27.5% gives an expected
// event count of ~3.85 in a 14-day window, so threshold = 3
// (formula_version 1) produced positive rate ~76% — the label barely
// discriminated. The empirical cumulative distribution showed:
//
//   threshold ≥7 → 25.4% positive (98 positives on the test slice)
//   threshold ≥8 → 18.9% positive (73 positives)
//
// `7` lands at the top of the operational 15–30% band and keeps more
// positives for any Phase 1 model evaluation. Revisit when the
// underlying Acute OR distribution shifts (new source_epoch, new
// thresholds, retrained acute classifier). Versioned via
// `chronicLoadFormulaVersion`.
const ChronicLoadMinAcuteDensity = 7

// ChronicLoadBaselineWindowDays — trailing window for the EWMA45
// baseline against which each candidate day's Recovery rolling_3d is
// scored. Matches plan §3.3 adaptive default.
const ChronicLoadBaselineWindowDays = 45

// ChronicLoadWarmupMinPaired — minimum count of eligible Recovery
// rolling_3d rows inside the current source_epoch (strictly before
// t+1) required to release the writer from baseline_warmup state.
// 30 matches the rolling-baseline-warmup convention used by Acute
// Risk and elsewhere in scoring.go.
const ChronicLoadWarmupMinPaired = 30
