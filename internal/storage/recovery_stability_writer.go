// Package storage — Recovery Stability writer.
//
// First real consumer of the Phase 0 redesign storage layer. Reads
// sleep rows from daily_scores, computes per-night eligibility via
// internal/health.ComputeSleepEfficiency, and emits target_snapshots,
// feature_snapshots, and naive_baselines for the Recovery Stability
// sub-score per READINESS_REDESIGN_PLAN.md §4.2.
//
// Not auto-fired from ingest. Call BackfillRecoveryStabilitySnapshots
// explicitly from backfill jobs or admin endpoints. The writer is safe
// to re-run — every Save* underneath upserts on its PK.
//
// Time anchor per plan §3.2: features for row dated `t` use only data
// available by end of day `t`; targets cover strictly-later windows
// (night t+1 for daily_point, nights t+1..t+3 for rolling_3d).
//
// Source-epoch handling: each row resolves its epoch at write time.
// Baseline windows are clipped to the current epoch's start_date so
// pre-epoch history does not bleed into post-epoch baselines (plan
// §3.4). Only `initial` is in play today; the clipping is here so
// future epochs are correct from day one.

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"health-receiver/internal/health"
)

// recoveryStabilityFormulaVersion bumps when the eligibility tree or
// target value formula changes. Reading code can use this to invalidate
// or compare old snapshots.
//
// Version history:
//
//	1 — initial release. `sleep_total_out_of_range` covered both
//	    Total==nil (no source row) and present-but-implausible values.
//	2 — split nil-Total into the new `sleep_data_missing` reason so
//	    data gaps are operationally distinguishable from short/long
//	    nights. Eligibility outcome (eligible bool) is unchanged
//	    between v1 and v2; only the `eligibility_reason` text differs
//	    for the affected rows.
//	3 — add a narrow 3.95h tolerance around the nominal 4.0h minimum
//	    nightly sleep cutoff. This is a numerical boundary fix for
//	    source-rounded values such as 3.99h, not a model change.
const recoveryStabilityFormulaVersion = 3

// recoveryStabilityFeatureVersion bumps when the feature set changes.
// Separate from formula_version because feature surface and target
// formula evolve independently.
const recoveryStabilityFeatureVersion = 3

// ewmaWindowAdaptive and ewmaWindowSlow encode the §3.3 decision: 45d
// adaptive baseline, 180d slow baseline. Effective windows; α is
// derived as 2/(N+1) at use sites.
const (
	ewmaWindowAdaptive = 45
	ewmaWindowSlow     = 180
)

const isoDate = "2006-01-02"

// LoadSleepRows pulls nullable sleep columns from daily_scores for the
// inclusive date range [from, to]. Date strings are YYYY-MM-DD; the
// daily_scores.date column is TEXT under the same convention.
//
// Rows are returned in ascending date order. A missing date in the
// range is omitted (not synthesised) — the caller decides what to do
// with gaps, typically marking those dates ineligible.
func (s *DB) LoadSleepRows(from, to string) ([]health.SleepRow, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT date, sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_awake, sleep_unspecified
		  FROM daily_scores
		 WHERE date BETWEEN $1 AND $2
		 ORDER BY date ASC
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("LoadSleepRows: %w", err)
	}
	defer rows.Close()
	var out []health.SleepRow
	for rows.Next() {
		var r health.SleepRow
		var total, deep, rem, core, awake, unsp *float32
		if err := rows.Scan(&r.Date, &total, &deep, &rem, &core, &awake, &unsp); err != nil {
			return nil, fmt.Errorf("LoadSleepRows scan: %w", err)
		}
		// daily_scores stores sleep_* as REAL (float32 in pgx). Lift to
		// float64 for downstream math; preserve NULL → nil pointer.
		r.Total = liftFloat(total)
		r.Deep = liftFloat(deep)
		r.REM = liftFloat(rem)
		r.Core = liftFloat(core)
		r.Awake = liftFloat(awake)
		r.Unspecified = liftFloat(unsp)
		out = append(out, r)
	}
	return out, rows.Err()
}

func liftFloat(p *float32) *float64 {
	if p == nil {
		return nil
	}
	v := float64(*p)
	return &v
}

// BackfillRecoveryStabilitySnapshots writes Recovery Stability target
// snapshots, feature snapshots, and naive baselines for every date in
// [from, to]. Idempotent. Returns the number of rows touched and the
// first error encountered (subsequent dates still attempt; the error
// is the first non-nil for caller visibility).
//
// The load window extends ewmaWindowSlow days before `from` and 3 days
// after `to` so the slow baseline has its full lookback and the
// rolling_3d target windows can read all three forward nights. Outside
// data is still bounded by the rows actually present in daily_scores.
func (s *DB) BackfillRecoveryStabilitySnapshots(from, to string) (int, error) {
	if err := validateReadinessDateRange(from, to, "BackfillRecoveryStabilitySnapshots"); err != nil {
		return 0, err
	}
	epochs, err := s.loadSourceEpochRunCache()
	if err != nil {
		return 0, err
	}
	return s.backfillRecoveryStabilitySnapshots(from, to, epochs)
}

func (s *DB) backfillRecoveryStabilitySnapshots(from, to string, epochs *sourceEpochRunCache) (int, error) {
	return s.backfillRecoveryStabilitySnapshotsForMaintenance(from, to, "", "", epochs)
}

// backfillRecoveryStabilitySnapshotsForMaintenance retains the full-run
// observation horizon when maintenance dispatches one target date at a time.
// Candidate maturity uses the furthest observed date, including when a night
// inside its forward window is missing.
func (s *DB) backfillRecoveryStabilitySnapshotsForMaintenance(from, to, observationFrom, observationThrough string, epochs *sourceEpochRunCache) (int, error) {
	fromT, err := time.Parse(isoDate, from)
	if err != nil {
		return 0, fmt.Errorf("BackfillRecoveryStabilitySnapshots: parse from: %w", err)
	}
	toT, err := time.Parse(isoDate, to)
	if err != nil {
		return 0, fmt.Errorf("BackfillRecoveryStabilitySnapshots: parse to: %w", err)
	}
	if toT.Before(fromT) {
		return 0, fmt.Errorf("BackfillRecoveryStabilitySnapshots: to %q before from %q", to, from)
	}

	observationFromTime := fromT
	if observationFrom != "" {
		var parseErr error
		observationFromTime, parseErr = time.Parse(isoDate, observationFrom)
		if parseErr != nil {
			return 0, fmt.Errorf("BackfillRecoveryStabilitySnapshots: parse observation start: %w", parseErr)
		}
		if observationFromTime.After(fromT) {
			return 0, fmt.Errorf("BackfillRecoveryStabilitySnapshots: observation start %q after target start %q", observationFrom, from)
		}
	}
	observationThroughTime := toT
	if observationThrough != "" {
		var parseErr error
		observationThroughTime, parseErr = time.Parse(isoDate, observationThrough)
		if parseErr != nil {
			return 0, fmt.Errorf("BackfillRecoveryStabilitySnapshots: parse observation horizon: %w", parseErr)
		}
		if observationThroughTime.Before(toT) {
			return 0, fmt.Errorf("BackfillRecoveryStabilitySnapshots: observation horizon %q before target end %q", observationThrough, to)
		}
	}
	loadFrom := fromT.AddDate(0, 0, -ewmaWindowSlow).Format(isoDate)
	loadTo := toT.AddDate(0, 0, 3).Format(isoDate)
	rows, err := s.LoadSleepRows(loadFrom, loadTo)
	if err != nil {
		return 0, err
	}
	latestObservedSleepDate := ""
	archByDate, err := s.LoadSleepArchitectureFeatures(from, to)
	if err != nil {
		return 0, err
	}

	// Index rows by date for O(1) lookups during feature/target gather.
	byDate := make(map[string]health.SleepRow, len(rows))
	effByDate := make(map[string]health.SleepEfficiencyResult, len(rows))
	captureByDate := make(map[string]health.SleepCaptureConfidenceResult, len(rows))
	for _, r := range rows {
		byDate[r.Date] = r
		effByDate[r.Date] = health.ComputeSleepEfficiency(r)
		captureByDate[r.Date] = health.ComputeSleepCaptureConfidence(r)
		if r.Date > latestObservedSleepDate {
			latestObservedSleepDate = r.Date
		}
	}
	if observationFrom != "" || observationThrough != "" {
		// Recovery candidate maturity depends on the maximum observed date
		// in the original whole-run horizon. Read only that scalar instead
		// of loading the entire range's sleep rows for each maintenance unit.
		wholeLoadFrom := observationFromTime.AddDate(0, 0, -ewmaWindowSlow).Format(isoDate)
		wholeLoadTo := observationThroughTime.AddDate(0, 0, 3).Format(isoDate)
		ctx, cancel := queryCtx()
		err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(date), '') FROM daily_scores WHERE date BETWEEN $1 AND $2`, wholeLoadFrom, wholeLoadTo).Scan(&latestObservedSleepDate)
		cancel()
		if err != nil {
			return 0, fmt.Errorf("read latest observed sleep date: %w", err)
		}
	}

	written := 0
	var firstErr error
	for d := fromT; !d.After(toT); d = d.AddDate(0, 0, 1) {
		date := d.Format(isoDate)
		if err := s.writeRecoveryStabilityRow(context.Background(), d, date, epochs, byDate, effByDate, captureByDate, archByDate[date], latestObservedSleepDate); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		written++
	}
	return written, firstErr
}

func (s *DB) writeRecoveryStabilityRow(
	ctx context.Context,
	t time.Time,
	date string,
	epochs *sourceEpochRunCache,
	byDate map[string]health.SleepRow,
	effByDate map[string]health.SleepEfficiencyResult,
	captureByDate map[string]health.SleepCaptureConfidenceResult,
	archFields SleepArchitectureFeatureFields,
	latestObservedSleepDate string,
) error {

	epoch, epochStart := epochs.resolve(date) // empty epoch start means no clipping

	// --- Daily-point target: night t+1 (i.e., daily_scores row for t+1) ---
	tp1 := t.AddDate(0, 0, 1).Format(isoDate)
	dailyTarget := targetFromEff(effByDate[tp1], byDate[tp1], sleepCaptureForDate(captureByDate, tp1))

	days, rollingCoverage, candidateCoverage := recoveryTargetWindow(t, effByDate, byDate, captureByDate, latestObservedSleepDate)
	labels, err := s.chipTargets(ctx, chipTargetWindow{date: date, subScore: SubScoreRecoveryStability, days: days, latestSleep: latestObservedSleepDate}, TargetKindDailyPoint, TargetKindRolling3d, TargetKindRolling3dCandidate2of3)
	if err != nil {
		return err
	}
	// --- Feature payload: data strictly ≤ end of day `t` ---
	w, err := s.chipFeatureWindow(ctx, t, epochStart, ewmaLookbackDays(ewmaWindowSlow), sleepEfficiencyLookup(effByDate))
	if err != nil {
		return err
	}
	features, err := s.recoveryFeatureContext(ctx, t, epochStart, byDate, effByDate, captureByDate, archFields)
	if err != nil {
		return err
	}
	features.PrevEfficiency, features.Mean7d, features.EWMA45, features.EWMA180 = w.Today, w.Mean7, w.Adaptive, w.Slow
	features.EligibleCount7d, features.EligibleCount45d, features.EligibleCount180d = w.N7, w.AdaptiveN, w.SlowN
	features.WarmupComplete45d, features.WarmupComplete180d = w.AdaptiveN >= ewmaWindowAdaptive/2, w.SlowN >= ewmaWindowSlow/3
	featuresJSON, err := json.Marshal(features)
	if err != nil {
		return fmt.Errorf("marshal features %s: %w", date, err)
	}

	// --- Naive baselines (for both daily_point and rolling_3d targets) ---
	naivePredictions, err := s.chipBaselines(ctx, t, epochStart, SubScoreRecoveryStability, sleepEfficiencyLookup(effByDate), TargetKindDailyPoint, TargetKindRolling3d, TargetKindRolling3dCandidate2of3)
	if err != nil {
		return err
	}

	// Persist.
	if err := s.SaveTargetSnapshot(TargetSnapshot{
		Date:              date,
		SubScore:          SubScoreRecoveryStability,
		TargetKind:        TargetKindDailyPoint,
		TargetValue:       labels[TargetKindDailyPoint].Value,
		Eligible:          labels[TargetKindDailyPoint].Eligible,
		EligibilityReason: labels[TargetKindDailyPoint].Reason,
		DataCoverage:      dailyTarget.Coverage,
		SourceEpoch:       epoch,
		FormulaVersion:    recoveryStabilityFormulaVersion,
	}); err != nil {
		return fmt.Errorf("save daily_point target %s: %w", date, err)
	}

	if err := s.SaveTargetSnapshot(TargetSnapshot{
		Date:              date,
		SubScore:          SubScoreRecoveryStability,
		TargetKind:        TargetKindRolling3d,
		TargetValue:       labels[TargetKindRolling3d].Value,
		Eligible:          labels[TargetKindRolling3d].Eligible,
		EligibilityReason: labels[TargetKindRolling3d].Reason,
		DataCoverage:      rollingCoverage,
		SourceEpoch:       epoch,
		FormulaVersion:    recoveryStabilityFormulaVersion,
	}); err != nil {
		return fmt.Errorf("save rolling_3d target %s: %w", date, err)
	}

	if err := s.SaveTargetSnapshot(TargetSnapshot{
		Date:              date,
		SubScore:          SubScoreRecoveryStability,
		TargetKind:        TargetKindRolling3dCandidate2of3,
		TargetValue:       labels[TargetKindRolling3dCandidate2of3].Value,
		Eligible:          labels[TargetKindRolling3dCandidate2of3].Eligible,
		EligibilityReason: labels[TargetKindRolling3dCandidate2of3].Reason,
		DataCoverage:      candidateCoverage,
		SourceEpoch:       epoch,
		FormulaVersion:    recoveryStabilityFormulaVersion,
	}); err != nil {
		return fmt.Errorf("save rolling_3d_candidate_2of3 target %s: %w", date, err)
	}

	if err := s.SaveFeatureSnapshot(FeatureSnapshot{
		Date:           date,
		SubScore:       SubScoreRecoveryStability,
		Features:       featuresJSON,
		SourceEpoch:    epoch,
		FeatureVersion: recoveryStabilityFeatureVersion,
	}); err != nil {
		return fmt.Errorf("save features %s: %w", date, err)
	}

	for _, b := range naivePredictions {
		nb := NaiveBaseline{
			Date:           date,
			SubScore:       SubScoreRecoveryStability,
			TargetKind:     b.TargetKind,
			BaselineKind:   b.BaselineKind,
			PredictedValue: b.Value,
			Reason:         b.Reason,
			SourceEpoch:    epoch,
			FormulaVersion: recoveryStabilityFormulaVersion,
		}
		if err := s.SaveNaiveBaseline(nb); err != nil {
			return fmt.Errorf("save baseline %s/%s/%s: %w", date, b.TargetKind, b.BaselineKind, err)
		}
	}
	return nil
}

// targetWriteSpec bundles the four fields needed to call SaveTargetSnapshot
// for one target_kind. Built once per (date, target_kind) and passed in.
type targetWriteSpec struct {
	Value    *float64
	Eligible bool
	Reason   string
	Coverage []byte
}

// targetFromEff turns a single-night SleepEfficiencyResult into the
// target spec for `daily_point`.
func targetFromEff(
	eff health.SleepEfficiencyResult,
	row health.SleepRow,
	capture health.SleepCaptureConfidenceResult,
) targetWriteSpec {
	// If the row simply isn't in daily_scores at all, eff is the zero
	// value; mark that explicitly so we don't write a spurious eligible
	// row when both eff.Efficiency and row.Total are nil.
	if row.Date == "" {
		return targetWriteSpec{
			Eligible: false,
			Reason:   health.SleepEligibilitySleepTotalOutOfRange,
			Coverage: mustMarshal(map[string]any{
				"reason_detail":            "daily_scores row missing for target date",
				"sleep_capture_class":      capture.Class,
				"sleep_capture_confidence": capture.Confidence,
				"sleep_capture_reason":     capture.Reason,
				"sleep_capture_low":        capture.LowConfidence,
			}),
		}
	}
	cov := mustMarshal(map[string]any{
		"sleep_total":              row.Total,
		"sleep_awake":              row.Awake,
		"sleep_unspecified":        row.Unspecified,
		"staged_present":           row.Deep != nil || row.REM != nil || row.Core != nil,
		"sleep_capture_class":      capture.Class,
		"sleep_capture_confidence": capture.Confidence,
		"sleep_capture_reason":     capture.Reason,
		"sleep_capture_low":        capture.LowConfidence,
	})
	return targetWriteSpec{
		Value:    eff.Efficiency,
		Eligible: eff.Eligible,
		Reason:   eff.EligibilityReason,
		Coverage: cov,
	}
}

func recoveryTargetWindow(t time.Time, eff map[string]health.SleepEfficiencyResult, rows map[string]health.SleepRow, captures map[string]health.SleepCaptureConfidenceResult, latest string) ([]chipTargetDay, json.RawMessage, json.RawMessage) {
	dates, reasons, missing := []string{}, []string{}, []string{}
	classes, captureReasons := []string{}, []string{}
	confidences := []float64{}
	var days []chipTargetDay
	eligible, low := 0, 0
	for offset := 1; offset <= 3; offset++ {
		day := t.AddDate(0, 0, offset).Format(isoDate)
		dates = append(dates, day)
		c := sleepCaptureForDate(captures, day)
		classes = append(classes, c.Class)
		captureReasons = append(captureReasons, c.Reason)
		confidences = append(confidences, c.Confidence)
		if c.LowConfidence {
			low++
		}
		if rows[day].Date == "" {
			missing = append(missing, day)
			reasons = append(reasons, health.SleepEligibilitySleepTotalOutOfRange)
			continue
		}
		e := eff[day]
		reasons = append(reasons, e.EligibilityReason)
		days = append(days, chipTargetDay{Date: day, Value: e.Efficiency, Eligible: e.Eligible, Reason: e.EligibilityReason})
		if e.Eligible && e.Efficiency != nil {
			eligible++
		}
	}
	coverage := map[string]any{"target_dates": dates, "per_day_reason": reasons, "eligible_count": eligible,
		"per_day_capture_class": classes, "per_day_capture_reason": captureReasons, "per_day_capture_confidence": confidences,
		"low_capture_confidence_count": low, "strict_rolling_eligibility": eligible == 3, "candidate_2of3_eligible_count": eligible}
	if eligible < 3 {
		coverage["missing_dates"] = missing
	}
	strict := mustMarshal(coverage)
	coverage["missing_dates"] = missing
	coverage["candidate_2of3_eligible"] = eligible >= 2
	coverage["candidate_2of3_value_days"] = eligible
	coverage["candidate_2of3_window_mature"] = windowMature(dates, latest)
	coverage["latest_observed_sleep_date"] = latest
	return days, strict, mustMarshal(coverage)
}

func windowMature(dates []string, latestObservedDate string) bool {
	if latestObservedDate == "" {
		return false
	}
	for _, d := range dates {
		if d > latestObservedDate {
			return false
		}
	}
	return true
}

// --- Features ----------------------------------------------------------

type recoveryFeatures struct {
	SleepArchitectureFeatureFields
	PrevEfficiency               *float64       `json:"prev_efficiency,omitempty"`
	Mean7d                       *float64       `json:"sleep_eff_mean_7d,omitempty"`
	EWMA45                       *float64       `json:"sleep_eff_ewma_45d,omitempty"`
	EWMA180                      *float64       `json:"sleep_eff_ewma_180d,omitempty"`
	SleepDebt7dHours             *float64       `json:"sleep_debt_7d_hours,omitempty"`
	SleepCaptureClass            string         `json:"sleep_capture_class,omitempty"`
	SleepCaptureConfidence       *float64       `json:"sleep_capture_confidence,omitempty"`
	SleepCaptureReason           string         `json:"sleep_capture_reason,omitempty"`
	SleepCaptureLow              bool           `json:"sleep_capture_low"`
	SleepCaptureClassCounts7d    map[string]int `json:"sleep_capture_class_counts_7d"`
	SleepCaptureLowDays7d        int            `json:"sleep_capture_low_days_7d"`
	SleepCaptureMeanConfidence7d *float64       `json:"sleep_capture_mean_confidence_7d,omitempty"`
	EligibleCount7d              int            `json:"eligible_count_7d"`
	EligibleCount45d             int            `json:"eligible_count_45d"`
	EligibleCount180d            int            `json:"eligible_count_180d"`
	WarmupComplete45d            bool           `json:"warmup_complete_45d"`
	WarmupComplete180d           bool           `json:"warmup_complete_180d"`
}

func sleepCaptureForDate(
	captureByDate map[string]health.SleepCaptureConfidenceResult,
	date string,
) health.SleepCaptureConfidenceResult {
	if capture, ok := captureByDate[date]; ok && capture.Class != "" {
		return capture
	}
	return health.ComputeSleepCaptureConfidence(health.SleepRow{Date: date})
}

// sleepEfficiencyLookup adapts a map of SleepEfficiencyResult to the
// DailyValueLookup contract used by the shared windowMean/windowEWMA
// helpers in sub_score_windows.go. Ineligible nights and absent dates
// both return (nil, false) so the window math skips them uniformly.
func sleepEfficiencyLookup(eff map[string]health.SleepEfficiencyResult) DailyValueLookup {
	return func(date string) (*float64, bool) {
		e, ok := eff[date]
		if !ok || !e.Eligible || e.Efficiency == nil {
			return nil, false
		}
		return e.Efficiency, true
	}
}

// --- Naive baselines ----------------------------------------------------

type naiveBaselineRow struct {
	TargetKind   string
	BaselineKind string
	Value        *float64
	// Reason explains Value == nil. Empty when Value != nil. Must
	// be one of the BaselineReason* enum values otherwise — see
	// chip_baseline_v1 for the rule that picks it.
	Reason string
}

// --- Helpers -----------------------------------------------------------

// lookupEpochStart returns the start_date of the given epoch_id, or ""
// when the epoch isn't found. Used to clip baseline lookbacks at epoch
// boundaries. A cache could be added later; in Phase 0 the catalogue
// is tiny so a per-row query is fine.
func (s *DB) lookupEpochStart(epochID string) string {
	if epochID == "" || epochID == SentinelSourceEpoch {
		return ""
	}
	ctx, cancel := queryCtx()
	defer cancel()
	var startDate string
	if err := s.pool.QueryRow(ctx,
		`SELECT start_date FROM source_epochs WHERE epoch_id = $1`,
		epochID,
	).Scan(&startDate); err != nil {
		return ""
	}
	return startDate
}

func ptrFloat(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// Inputs are simple maps with primitive values; marshal cannot
		// fail on them. Surface as a non-empty fallback so callers do
		// not pass nil JSON to SaveTargetSnapshot.
		return fmt.Appendf(nil, `{"_marshal_error":%q}`, err.Error())
	}
	return b
}
