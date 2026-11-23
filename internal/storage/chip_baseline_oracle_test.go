package storage

import (
	"health-receiver/internal/health"
	"math"
	"sort"
	"time"
)

func buildRecoveryNaiveBaselines(t time.Time, epochStart string, eff map[string]health.SleepEfficiencyResult) []naiveBaselineRow {
	out := make([]naiveBaselineRow, 0, 12)

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
	out = append(out, appendBaselineSet(BaselineKindPersistenceYesterday, persist, classifyBaselineNullReason(t, 0, epochStart),
		TargetKindDailyPoint, TargetKindRolling3d, TargetKindRolling3dCandidate2of3)...)

	lookup := sleepEfficiencyLookup(eff)

	mean7, _ := windowMean(t, 7, epochStart, lookup)
	out = append(out, appendBaselineSet(BaselineKindRolling7dMean, mean7, classifyBaselineNullReason(t, 6, epochStart),
		TargetKindDailyPoint, TargetKindRolling3d, TargetKindRolling3dCandidate2of3)...)

	mean30, _ := windowMean(t, 30, epochStart, lookup)
	out = append(out, appendBaselineSet(BaselineKindRolling30dMean, mean30, classifyBaselineNullReason(t, 29, epochStart),
		TargetKindDailyPoint, TargetKindRolling3d, TargetKindRolling3dCandidate2of3)...)

	ewma45, _ := windowEWMA(t, ewmaWindowAdaptive, epochStart, lookup)
	out = append(out, appendBaselineSet(BaselineKindEWMA45d, ewma45,
		classifyBaselineNullReason(t, ewmaLookbackDays(ewmaWindowAdaptive), epochStart),
		TargetKindDailyPoint, TargetKindRolling3d, TargetKindRolling3dCandidate2of3)...)

	return out
}

// appendBaselinePair emits the same value/reason pair for two target
// kinds. Centralises the "reason only when value is nil" rule so the
// individual callers stay tabular.
func appendBaselinePair(tk1, tk2, baselineKind string, val *float64, nullReason string) []naiveBaselineRow {
	return appendBaselineSet(baselineKind, val, nullReason, tk1, tk2)
}

// appendBaselineSet emits the same value/reason pair for every listed
// target kind.
func appendBaselineSet(baselineKind string, val *float64, nullReason string, targetKinds ...string) []naiveBaselineRow {
	reason := ""
	if val == nil {
		reason = nullReason
	}
	out := make([]naiveBaselineRow, 0, len(targetKinds))
	for _, tk := range targetKinds {
		out = append(out, naiveBaselineRow{
			TargetKind:   tk,
			BaselineKind: baselineKind,
			Value:        val,
			Reason:       reason,
		})
	}
	return out
}

// --- Naive baselines ----------------------------------------------------

func buildPassiveEfficiencyNaiveBaselines(t time.Time, epochStart string, verdict map[string]health.WalkingHREligibilityResult) []naiveBaselineRow {
	out := make([]naiveBaselineRow, 0, 8)
	lookup := walkingHRLookup(verdict)

	var persist *float64
	if v, ok := verdict[t.Format(isoDate)]; ok && v.Eligible && v.Value != nil {
		persist = ptrFloat(*v.Value)
	}
	// Each classifier call passes the actual earliest-day offset for
	// that baseline's lookback — see classifyBaselineNullReason's
	// docstring for the per-baseline numbers.
	out = append(out, appendBaselinePair(TargetKindDailyPoint, TargetKindRolling3d,
		BaselineKindPersistenceYesterday, persist, classifyBaselineNullReason(t, 0, epochStart))...)

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

// priorEventBaseRate averages the eligible event labels for the
// `windowDays` dates strictly before `t`. Dates older than
// `epochStart` (when non-empty) are skipped so the baseline resets
// at every source_epoch boundary — without this, the now-unconditional
// classifier baseline writes (since #110) would leak labels from a
// previous epoch into the first ~90 days of a new one. Returns nil
// when no eligible labels fall in the (clipped) window.
func priorEventBaseRate(t time.Time, windowDays int, epochStart string, eventByDate map[string]int) *float64 {
	var sum, n int
	for i := 1; i <= windowDays; i++ {
		ds := t.AddDate(0, 0, -i).Format(isoDate)
		if epochStart != "" && ds < epochStart {
			continue
		}
		v, ok := eventByDate[ds]
		if !ok {
			continue
		}
		sum += v
		n++
	}
	if n == 0 {
		return nil
	}
	r := float64(sum) / float64(n)
	return &r
}

// percentile returns the requested `p`-th percentile of `xs` using
// linear interpolation between order statistics — the standard
// numpy / R `linear` definition. `p` is in [0, 100]; out-of-range
// values panic in callers, but the writer always passes 80 so we
// don't guard.
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	if len(sorted) == 1 {
		return sorted[0]
	}
	rank := (p / 100.0) * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	frac := rank - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

func buildPassiveEfficiencyFeatures(
	t time.Time,
	epochStart string,
	verdict map[string]health.WalkingHREligibilityResult,
) passiveEfficiencyFeatures {
	var out passiveEfficiencyFeatures
	lookup := walkingHRLookup(verdict)

	// Previous walking HR: eligibility result at date t (most recent
	// observation by end of day t).
	if v, ok := verdict[t.Format(isoDate)]; ok && v.Eligible && v.Value != nil {
		out.PrevWalkingHR = ptrFloat(*v.Value)
	}

	mean7, n7 := windowMean(t, 7, epochStart, lookup)
	out.Mean7d = mean7
	out.EligibleCount7d = n7

	mean30, _ := windowMean(t, 30, epochStart, lookup)
	out.Mean30d = mean30

	ewma45, n45 := windowEWMA(t, ewmaWindowAdaptive, epochStart, lookup)
	out.EWMA45 = ewma45
	out.EligibleCount45d = n45
	out.WarmupComplete45 = n45 >= ewmaWindowAdaptive/2

	ewma180, n180 := windowEWMA(t, ewmaWindowSlow, epochStart, lookup)
	out.EWMA180 = ewma180
	out.EligibleCount180d = n180
	out.WarmupComplete180 = n180 >= ewmaWindowSlow/3

	if out.PrevWalkingHR != nil {
		if ewma45 != nil {
			d := *out.PrevWalkingHR - *ewma45
			out.DeltaVsEWMA45 = &d
		}
		if ewma180 != nil {
			d := *out.PrevWalkingHR - *ewma180
			out.DeltaVsEWMA180 = &d
		}
	}
	return out
}

func buildRecoveryFeatures(
	t time.Time,
	epochStart string,
	byDate map[string]health.SleepRow,
	effByDate map[string]health.SleepEfficiencyResult,
	captureByDate map[string]health.SleepCaptureConfidenceResult,
	archFields SleepArchitectureFeatureFields,
) recoveryFeatures {
	out := recoveryFeatureContext(t, epochStart, byDate, effByDate, captureByDate, archFields)
	// Previous eff: eligibility result for daily_scores row dated `t`
	// itself (the most recent completed sleep). Plan §3.2: features for
	// row `t` may read data up to end of day `t`, including the sleep
	// that ended on the morning of `t` (Apple Health convention assigns
	// that sleep to wake-date `t`).
	if eff, ok := effByDate[t.Format(isoDate)]; ok && eff.Eligible && eff.Efficiency != nil {
		out.PrevEfficiency = ptrFloat(*eff.Efficiency)
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

	return out
}

func buildChronicLoadFeatures(
	t time.Time,
	epochStart string,
	recoveryByDate map[string]recoveryRolling3dRow,
	recoveryLookup DailyValueLookup,
	pairedCount int,
	warmupMet bool,
	archFields SleepArchitectureFeatureFields,
) chronicLoadFeatures {
	out := chronicLoadFeatures{
		PairedCountToT: pairedCount,
		WarmupMet:      warmupMet,
	}
	out.SleepArchitectureFeatureFields = archFields
	// Today's Recovery 3d-roll, when eligible.
	if r, ok := recoveryByDate[t.Format(isoDate)]; ok && r.Eligible && r.Value != nil {
		out.RecoveryToday = ptrFloat(*r.Value)
	}
	// EWMA45 baseline at end-of-day-t (inclusive at t but excluding
	// t+1 onwards). Distinct from the per-candidate baselines used for
	// the primary label — both are honest reflections of state at t.
	tPlusOne := t.AddDate(0, 0, 1)
	mean, sd, n45 := windowStatsBefore(tPlusOne, health.ChronicLoadBaselineWindowDays, epochStart, recoveryLookup)
	out.BaselineMean45 = mean
	out.BaselineSD45 = sd
	out.EligibleCount45 = n45
	out.WarmupComplete45 = n45 >= health.ChronicLoadBaselineWindowDays/2

	if out.RecoveryToday != nil {
		out.BaselineZToday = zScoreOrNil(*out.RecoveryToday, mean, sd)
	}

	// 180-day slow window — count only, used for chronic-drift warmup
	// indicator. SD beyond a year of history wasn't requested in the
	// feature spec.
	_, _, n180 := windowStatsBefore(tPlusOne, ewmaWindowSlow, epochStart, recoveryLookup)
	out.EligibleCount180 = n180
	out.WarmupComplete180 = n180 >= ewmaWindowSlow/3
	return out
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

// rolling3dCandidate2of3Target is an evidence-only Recovery target.
// It keeps the same forward label window as strict rolling_3d, but
// allows one ineligible night and averages the eligible nights when at
// least two are present. No product path should read this target kind.
func rolling3dCandidate2of3Target(
	t time.Time,
	effByDate map[string]health.SleepEfficiencyResult,
	byDate map[string]health.SleepRow,
	captureByDate map[string]health.SleepCaptureConfidenceResult,
	latestObservedSleepDate string,
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

	coverage := map[string]any{
		"target_dates":                  dates,
		"missing_dates":                 missing,
		"per_day_reason":                reasons,
		"eligible_count":                len(values),
		"per_day_capture_class":         captureClasses,
		"per_day_capture_reason":        captureReasons,
		"per_day_capture_confidence":    captureConfidences,
		"low_capture_confidence_count":  lowConfidenceCount,
		"strict_rolling_eligibility":    len(values) == 3,
		"candidate_2of3_eligible_count": len(values),
		"candidate_2of3_eligible":       len(values) >= 2,
		"candidate_2of3_value_days":     len(values),
		"candidate_2of3_window_mature":  windowMature(dates, latestObservedSleepDate),
		"latest_observed_sleep_date":    latestObservedSleepDate,
	}
	if !windowMature(dates, latestObservedSleepDate) {
		return targetWriteSpec{
			Eligible: false,
			Reason:   EligibilitySleepDataMissing,
			Coverage: mustMarshal(coverage),
		}
	}
	if len(values) < 2 {
		return targetWriteSpec{
			Eligible: false,
			Reason:   firstBlockingReason(reasons),
			Coverage: mustMarshal(coverage),
		}
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean := sum / float64(len(values))
	return targetWriteSpec{
		Value:    &mean,
		Eligible: true,
		Reason:   health.SleepEligibilityOK,
		Coverage: mustMarshal(coverage),
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

// passiveRolling3dTarget enforces the all-three-eligible rule: rolling
// target is only eligible when every day in t+1..t+3 has a present and
// in-range walking HR. One out-of-range or missing day flips it.
func passiveRolling3dTarget(
	t time.Time,
	verdict map[string]health.WalkingHREligibilityResult,
	rowByDate map[string]health.WalkingHRRow,
) targetWriteSpec {
	dates := []string{
		t.AddDate(0, 0, 1).Format(isoDate),
		t.AddDate(0, 0, 2).Format(isoDate),
		t.AddDate(0, 0, 3).Format(isoDate),
	}
	values := make([]float64, 0, 3)
	reasons := make([]string, 0, 3)
	missing := make([]string, 0, 3)
	for _, d := range dates {
		v, hasVerdict := verdict[d]
		if !hasVerdict {
			missing = append(missing, d)
			reasons = append(reasons, health.PassiveEfficiencyNoWalkingHR)
			continue
		}
		reasons = append(reasons, v.EligibilityReason)
		if !v.Eligible || v.Value == nil {
			continue
		}
		values = append(values, *v.Value)
	}
	if len(values) < 3 {
		cov := mustMarshal(map[string]any{
			"target_dates":   dates,
			"missing_dates":  missing,
			"per_day_reason": reasons,
			"eligible_count": len(values),
		})
		return targetWriteSpec{
			Eligible: false,
			Reason:   firstPassiveBlockingReason(reasons),
			Coverage: cov,
		}
	}
	mean := (values[0] + values[1] + values[2]) / 3.0
	cov := mustMarshal(map[string]any{
		"target_dates":   dates,
		"per_day_reason": reasons,
		"eligible_count": 3,
		"per_day_value": map[string]*float64{
			dates[0]: rowValueOrNil(rowByDate, dates[0]),
			dates[1]: rowValueOrNil(rowByDate, dates[1]),
			dates[2]: rowValueOrNil(rowByDate, dates[2]),
		},
	})
	return targetWriteSpec{
		Value:    &mean,
		Eligible: true,
		Reason:   health.PassiveEfficiencyOK,
		Coverage: cov,
	}
}

// firstPassiveBlockingReason picks the most informative reason to
// surface when the rolling target is ineligible. Out-of-range is a
// stronger signal than "data missing" (it means the value we saw was
// implausible, not that we saw nothing), so it wins priority.
func firstPassiveBlockingReason(reasons []string) string {
	priority := map[string]int{
		health.PassiveEfficiencyWalkingHROutOfRange: 3,
		health.PassiveEfficiencyNoWalkingHR:         2,
		health.PassiveEfficiencyOK:                  0,
	}
	best := ""
	bestP := -1
	for _, r := range reasons {
		p, ok := priority[r]
		if !ok {
			p = 4
		}
		if p > bestP {
			bestP = p
			best = r
		}
	}
	if best == "" {
		return health.PassiveEfficiencyNoWalkingHR
	}
	return best
}

// recoveryStabilityPersonalSleepTargetH is the constant target nightly
// sleep duration used by the sleep-debt feature. Hard-coded for Phase 0
// (no per-user calibration). When/if user-specific targets land, this
// becomes a setting and feature_version bumps.
const recoveryStabilityPersonalSleepTargetH = 7.5

func recoveryFeatureContext(
	t time.Time,
	epochStart string,
	byDate map[string]health.SleepRow,
	effByDate map[string]health.SleepEfficiencyResult,
	captureByDate map[string]health.SleepCaptureConfidenceResult,
	archFields SleepArchitectureFeatureFields,
) recoveryFeatures {
	var out recoveryFeatures
	out.SleepArchitectureFeatureFields = archFields
	out.SleepCaptureClassCounts7d = map[string]int{}

	capture := sleepCaptureForDate(captureByDate, t.Format(isoDate))
	out.SleepCaptureClass = capture.Class
	out.SleepCaptureConfidence = ptrFloat(capture.Confidence)
	out.SleepCaptureReason = capture.Reason
	out.SleepCaptureLow = capture.LowConfidence

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
		capture := sleepCaptureForDate(captureByDate, d)
		out.SleepCaptureClassCounts7d[capture.Class]++
		confidenceSum += capture.Confidence
		confidenceDays++
		if capture.LowConfidence {
			out.SleepCaptureLowDays7d++
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
