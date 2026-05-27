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
const recoveryStabilityFormulaVersion = 2

// recoveryStabilityFeatureVersion bumps when the feature set changes.
// Separate from formula_version because feature surface and target
// formula evolve independently.
const recoveryStabilityFeatureVersion = 3

// recoveryStabilityPersonalSleepTargetH is the constant target nightly
// sleep duration used by the sleep-debt feature. Hard-coded for Phase 0
// (no per-user calibration). When/if user-specific targets land, this
// becomes a setting and feature_version bumps.
const recoveryStabilityPersonalSleepTargetH = 7.5

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

	loadFrom := fromT.AddDate(0, 0, -ewmaWindowSlow).Format(isoDate)
	loadTo := toT.AddDate(0, 0, 3).Format(isoDate)
	rows, err := s.LoadSleepRows(loadFrom, loadTo)
	if err != nil {
		return 0, err
	}
	archLoadFrom := fromT.AddDate(0, 0, -14).Format(isoDate)
	archByDate, err := s.LoadSleepArchitectureDays(archLoadFrom, to)
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
	}

	written := 0
	var firstErr error
	for d := fromT; !d.After(toT); d = d.AddDate(0, 0, 1) {
		date := d.Format(isoDate)
		if err := s.writeRecoveryStabilityRow(context.Background(), d, date, byDate, effByDate, captureByDate, archByDate); err != nil {
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
	byDate map[string]health.SleepRow,
	effByDate map[string]health.SleepEfficiencyResult,
	captureByDate map[string]health.SleepCaptureConfidenceResult,
	archByDate map[string]SleepArchitectureDay,
) error {
	_ = ctx // reserved for future use; all storage helpers below use their own queryCtx

	epoch, err := s.ResolveSourceEpoch(date)
	if err != nil {
		// ResolveSourceEpoch returns SentinelSourceEpoch instead of an
		// error on no-match; a real error is unexpected. Fail fast.
		return fmt.Errorf("resolve source_epoch for %s: %w", date, err)
	}
	epochStart := s.lookupEpochStart(epoch) // empty string when unknown — treat as no clip

	// --- Daily-point target: night t+1 (i.e., daily_scores row for t+1) ---
	tp1 := t.AddDate(0, 0, 1).Format(isoDate)
	dailyTarget := targetFromEff(effByDate[tp1], byDate[tp1], sleepCaptureForDate(captureByDate, tp1))

	// --- Rolling 3-day target: mean of eff(t+1), eff(t+2), eff(t+3) ---
	rollingTarget := rolling3dTarget(t, effByDate, byDate, captureByDate)

	// --- Feature payload: data strictly ≤ end of day `t` ---
	features := buildRecoveryFeatures(t, epochStart, byDate, effByDate, captureByDate, archByDate)
	featuresJSON, err := json.Marshal(features)
	if err != nil {
		return fmt.Errorf("marshal features %s: %w", date, err)
	}

	// --- Naive baselines (for both daily_point and rolling_3d targets) ---
	naivePredictions := buildRecoveryNaiveBaselines(t, epochStart, effByDate)

	// Persist.
	if err := s.SaveTargetSnapshot(TargetSnapshot{
		Date:              date,
		SubScore:          SubScoreRecoveryStability,
		TargetKind:        TargetKindDailyPoint,
		TargetValue:       dailyTarget.Value,
		Eligible:          dailyTarget.Eligible,
		EligibilityReason: dailyTarget.Reason,
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
		TargetValue:       rollingTarget.Value,
		Eligible:          rollingTarget.Eligible,
		EligibilityReason: rollingTarget.Reason,
		DataCoverage:      rollingTarget.Coverage,
		SourceEpoch:       epoch,
		FormulaVersion:    recoveryStabilityFormulaVersion,
	}); err != nil {
		return fmt.Errorf("save rolling_3d target %s: %w", date, err)
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

// rolling3dTarget computes the mean of eff(t+1), eff(t+2), eff(t+3).
// All three nights must be individually eligible. If any one fails,
// the rolling target is ineligible and the rolling row records which
// of the three nights blocked it. Avoids partial-window averages that
// would be silently biased toward whichever nights survived.
func rolling3dTarget(
	t time.Time,
	effByDate map[string]health.SleepEfficiencyResult,
	byDate map[string]health.SleepRow,
	captureByDate map[string]health.SleepCaptureConfidenceResult,
) targetWriteSpec {
	dates := []string{
		t.AddDate(0, 0, 1).Format(isoDate),
		t.AddDate(0, 0, 2).Format(isoDate),
		t.AddDate(0, 0, 3).Format(isoDate),
	}
	values := make([]float64, 0, 3)
	reasons := make([]string, 0, 3)
	missing := make([]string, 0, 3)
	captureClasses := make([]string, 0, 3)
	captureReasons := make([]string, 0, 3)
	captureConfidences := make([]float64, 0, 3)
	lowConfidenceCount := 0
	for _, d := range dates {
		capture := sleepCaptureForDate(captureByDate, d)
		captureClasses = append(captureClasses, capture.Class)
		captureReasons = append(captureReasons, capture.Reason)
		captureConfidences = append(captureConfidences, capture.Confidence)
		if capture.LowConfidence {
			lowConfidenceCount++
		}
		row, ok := byDate[d]
		if !ok || row.Date == "" {
			missing = append(missing, d)
			reasons = append(reasons, health.SleepEligibilitySleepTotalOutOfRange)
			continue
		}
		e := effByDate[d]
		reasons = append(reasons, e.EligibilityReason)
		if !e.Eligible || e.Efficiency == nil {
			continue
		}
		values = append(values, *e.Efficiency)
	}

	if len(values) < 3 {
		cov := mustMarshal(map[string]any{
			"target_dates":                  dates,
			"missing_dates":                 missing,
			"per_day_reason":                reasons,
			"eligible_count":                len(values),
			"per_day_capture_class":         captureClasses,
			"per_day_capture_reason":        captureReasons,
			"per_day_capture_confidence":    captureConfidences,
			"low_capture_confidence_count":  lowConfidenceCount,
			"strict_rolling_eligibility":    false,
			"candidate_2of3_eligible_count": len(values),
		})
		return targetWriteSpec{
			Eligible: false,
			Reason:   firstBlockingReason(reasons),
			Coverage: cov,
		}
	}
	mean := (values[0] + values[1] + values[2]) / 3.0
	cov := mustMarshal(map[string]any{
		"target_dates":                  dates,
		"per_day_reason":                reasons,
		"eligible_count":                3,
		"per_day_capture_class":         captureClasses,
		"per_day_capture_reason":        captureReasons,
		"per_day_capture_confidence":    captureConfidences,
		"low_capture_confidence_count":  lowConfidenceCount,
		"strict_rolling_eligibility":    true,
		"candidate_2of3_eligible_count": 3,
	})
	return targetWriteSpec{
		Value:    &mean,
		Eligible: true,
		Reason:   health.SleepEligibilityOK,
		Coverage: cov,
	}
}

// firstBlockingReason picks the most informative reason to surface as
// the rolling target's eligibility_reason when ≥1 night was ineligible.
// Priority: any non-ok reason wins over ok; out_of_range beats
// missing_awake_unknown beats coarse_only_source — surface the most
// definitive blocker.
func firstBlockingReason(reasons []string) string {
	priority := map[string]int{
		health.SleepEligibilitySleepTotalOutOfRange:  4,
		health.SleepEligibilityMissingAwakeUnknown:   3,
		health.SleepEligibilityCoarseOnlySource:      2,
		health.SleepEligibilityOKAwakeStructuralZero: 1,
		health.SleepEligibilityOK:                    0,
	}
	best := ""
	bestP := -1
	for _, r := range reasons {
		p, ok := priority[r]
		if !ok {
			p = 5 // unknown reason — surface it
		}
		if p > bestP {
			bestP = p
			best = r
		}
	}
	if best == "" {
		return health.SleepEligibilitySleepTotalOutOfRange
	}
	return best
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

func buildRecoveryFeatures(
	t time.Time,
	epochStart string,
	byDate map[string]health.SleepRow,
	effByDate map[string]health.SleepEfficiencyResult,
	captureByDate map[string]health.SleepCaptureConfidenceResult,
	archByDate map[string]SleepArchitectureDay,
) recoveryFeatures {
	var out recoveryFeatures
	out.SleepArchitectureFeatureFields = BuildSleepArchitectureFeatureFields(t, archByDate)
	out.SleepCaptureClassCounts7d = map[string]int{}

	// Previous eff: eligibility result for daily_scores row dated `t`
	// itself (the most recent completed sleep). Plan §3.2: features for
	// row `t` may read data up to end of day `t`, including the sleep
	// that ended on the morning of `t` (Apple Health convention assigns
	// that sleep to wake-date `t`).
	if eff, ok := effByDate[t.Format(isoDate)]; ok && eff.Eligible && eff.Efficiency != nil {
		out.PrevEfficiency = ptrFloat(*eff.Efficiency)
	}
	if capture, ok := captureByDate[t.Format(isoDate)]; ok {
		out.SleepCaptureClass = capture.Class
		out.SleepCaptureConfidence = ptrFloat(capture.Confidence)
		out.SleepCaptureReason = capture.Reason
		out.SleepCaptureLow = capture.LowConfidence
	}

	lookup := sleepEfficiencyLookup(effByDate)

	// 7-day mean efficiency over [t-6, t], eligible nights only.
	mean7, n7 := windowMean(t, 7, epochStart, lookup)
	out.Mean7d = mean7
	out.EligibleCount7d = n7

	// EWMA 45 and EWMA 180 over the eligible series back to epoch start.
	ewma45, n45 := windowEWMA(t, ewmaWindowAdaptive, epochStart, lookup)
	out.EWMA45 = ewma45
	out.EligibleCount45d = n45
	out.WarmupComplete45d = n45 >= ewmaWindowAdaptive/2 // soft warmup threshold

	ewma180, n180 := windowEWMA(t, ewmaWindowSlow, epochStart, lookup)
	out.EWMA180 = ewma180
	out.EligibleCount180d = n180
	out.WarmupComplete180d = n180 >= ewmaWindowSlow/3 // soft warmup threshold

	// Sleep debt over last 7 days: target * 7 − sum of sleep_total for
	// the eligible nights. Missing/ineligible nights contribute 0 to
	// the sum (i.e., counted as zero sleep against the target). This
	// makes debt sensitive to coverage gaps, which is intentional —
	// the eligible_count_7d feature lets downstream see how complete
	// the window was.
	var totalSlept float64
	var confidenceSum float64
	var confidenceDays int
	for i := range 7 {
		d := t.AddDate(0, 0, -i).Format(isoDate)
		if epochStart != "" && d < epochStart {
			continue
		}
		if capture, ok := captureByDate[d]; ok {
			out.SleepCaptureClassCounts7d[capture.Class]++
			confidenceSum += capture.Confidence
			confidenceDays++
			if capture.LowConfidence {
				out.SleepCaptureLowDays7d++
			}
		}
		row, ok := byDate[d]
		if !ok || row.Total == nil {
			continue
		}
		eff := effByDate[d]
		if !eff.Eligible {
			continue
		}
		totalSlept += *row.Total
	}
	if confidenceDays > 0 {
		out.SleepCaptureMeanConfidence7d = ptrFloat(confidenceSum / float64(confidenceDays))
	}
	debt := recoveryStabilityPersonalSleepTargetH*7 - totalSlept
	out.SleepDebt7dHours = ptrFloat(debt)

	return out
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
	// classifyBaselineNullReason for the rule that picks it.
	Reason string
}

func buildRecoveryNaiveBaselines(t time.Time, epochStart string, eff map[string]health.SleepEfficiencyResult) []naiveBaselineRow {
	out := make([]naiveBaselineRow, 0, 8)

	// Persistence — yesterday's efficiency predicts tomorrow's. For
	// daily_point at t+1 this is eff(t). For rolling_3d at t+1..t+3
	// this is also eff(t) (the most recent observed value).
	var persist *float64
	if e, ok := eff[t.Format(isoDate)]; ok && e.Eligible && e.Efficiency != nil {
		persist = ptrFloat(*e.Efficiency)
	}
	// Each classifier call passes the actual earliest-day offset for
	// that baseline's lookback — see classifyBaselineNullReason's
	// docstring for the per-baseline numbers.
	out = append(out, appendBaselinePair(TargetKindDailyPoint, TargetKindRolling3d,
		BaselineKindPersistenceYesterday, persist, classifyBaselineNullReason(t, 0, epochStart))...)

	lookup := sleepEfficiencyLookup(eff)

	mean7, _ := windowMean(t, 7, epochStart, lookup)
	out = append(out, appendBaselinePair(TargetKindDailyPoint, TargetKindRolling3d,
		BaselineKindRolling7dMean, mean7, classifyBaselineNullReason(t, 6, epochStart))...)

	mean30, _ := windowMean(t, 30, epochStart, lookup)
	out = append(out, appendBaselinePair(TargetKindDailyPoint, TargetKindRolling3d,
		BaselineKindRolling30dMean, mean30, classifyBaselineNullReason(t, 29, epochStart))...)

	ewma45, _ := windowEWMA(t, ewmaWindowAdaptive, epochStart, lookup)
	out = append(out, appendBaselinePair(TargetKindDailyPoint, TargetKindRolling3d,
		BaselineKindEWMA45d, ewma45,
		classifyBaselineNullReason(t, ewmaLookbackDays(ewmaWindowAdaptive), epochStart))...)

	return out
}

// appendBaselinePair emits the same value/reason pair for two target
// kinds. Centralises the "reason only when value is nil" rule so the
// individual callers stay tabular.
func appendBaselinePair(tk1, tk2, baselineKind string, val *float64, nullReason string) []naiveBaselineRow {
	reason := ""
	if val == nil {
		reason = nullReason
	}
	return []naiveBaselineRow{
		{TargetKind: tk1, BaselineKind: baselineKind, Value: val, Reason: reason},
		{TargetKind: tk2, BaselineKind: baselineKind, Value: val, Reason: reason},
	}
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
