// Package storage — Passive Efficiency writer.
//
// Second sub-score writer (plan §4.2). Target is the Apple-computed
// `walking_heart_rate_average` daily metric: per-event daily mean of
// HR during walking, classified by the OS. Operationalised as "how
// expensive is ordinary movement today" — a daily signal that exists
// on sedentary days too, unlike workout HR residual.
//
// Source rows live in metric_points, not daily_scores. We aggregate
// across `quality='ok'` rows per local date via AVG. Multiple sources
// on the same day (rare) collapse to one daily mean — the writer
// treats sources as interchangeable since Apple's daily aggregate is
// itself a cross-source-corrected value.
//
// Mirrors the Recovery Stability writer in shape: per-date targets
// for daily_point + rolling_3d, feature snapshot ≤ end of day t, and
// naive baselines (persistence, 7d mean, 30d mean, EWMA45). Shares
// windowMean / windowEWMA via the DailyValueLookup callback in
// sub_score_windows.go.

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"health-receiver/internal/health"
)

const passiveEfficiencyFormulaVersion = 1
const passiveEfficiencyFeatureVersion = 1

// walkingHRMetricName is the metric_points.metric_name key for Apple's
// daily walking HR aggregate. Mapped from HKQuantityTypeIdentifierWalkingHeartRateAverage
// in internal/applehealth/parse.go.
const walkingHRMetricName = "walking_heart_rate_average"

// LoadWalkingHRRows aggregates `walking_heart_rate_average` rows from
// metric_points to one daily mean per date over `quality='ok'` entries.
// Returns rows in ascending date order; dates with no data are omitted.
//
// Note: filtering by `quality='ok'` mirrors the read pattern used by
// briefing/aggregates code (see CLAUDE.md "Quality Validation"). Dates
// where all rows were flagged impossible/suspect therefore look the
// same to this writer as dates with no data at all — both surface as
// `no_walking_hr` rather than `walking_hr_out_of_range`. The eligibility
// distinction in the plan is about the *daily mean* exceeding the band,
// not about pre-filtered points.
func (s *DB) LoadWalkingHRRows(from, to string) ([]health.WalkingHRRow, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT SUBSTRING(date FROM 1 FOR 10) AS d, AVG(qty)::real
		  FROM metric_points
		 WHERE metric_name = $1
		   AND quality = 'ok'
		   AND SUBSTRING(date FROM 1 FOR 10) BETWEEN $2 AND $3
		 GROUP BY 1
		 ORDER BY 1 ASC
	`, walkingHRMetricName, from, to)
	if err != nil {
		return nil, fmt.Errorf("LoadWalkingHRRows: %w", err)
	}
	defer rows.Close()
	var out []health.WalkingHRRow
	for rows.Next() {
		var d string
		var avg float32
		if err := rows.Scan(&d, &avg); err != nil {
			return nil, fmt.Errorf("LoadWalkingHRRows scan: %w", err)
		}
		v := float64(avg)
		out = append(out, health.WalkingHRRow{Date: d, Value: &v})
	}
	return out, rows.Err()
}

// BackfillPassiveEfficiencySnapshots writes target snapshots, feature
// snapshots, and naive baselines for the Passive Efficiency sub-score
// over [from, to]. Idempotent. Not auto-fired from ingest.
//
// Load window extends ewmaWindowSlow days before `from` and 3 days
// after `to` so the slow baseline has its lookback and rolling_3d
// targets have all forward days available.
func (s *DB) BackfillPassiveEfficiencySnapshots(from, to string) (int, error) {
	fromT, err := time.Parse(isoDate, from)
	if err != nil {
		return 0, fmt.Errorf("BackfillPassiveEfficiencySnapshots: parse from: %w", err)
	}
	toT, err := time.Parse(isoDate, to)
	if err != nil {
		return 0, fmt.Errorf("BackfillPassiveEfficiencySnapshots: parse to: %w", err)
	}
	if toT.Before(fromT) {
		return 0, fmt.Errorf("BackfillPassiveEfficiencySnapshots: to %q before from %q", to, from)
	}

	loadFrom := fromT.AddDate(0, 0, -ewmaWindowSlow).Format(isoDate)
	loadTo := toT.AddDate(0, 0, 3).Format(isoDate)
	loaded, err := s.LoadWalkingHRRows(loadFrom, loadTo)
	if err != nil {
		return 0, err
	}

	// Index by date and pre-compute eligibility for fast lookup.
	rowByDate := make(map[string]health.WalkingHRRow, len(loaded))
	verdictByDate := make(map[string]health.WalkingHREligibilityResult, len(loaded))
	for _, r := range loaded {
		rowByDate[r.Date] = r
		verdictByDate[r.Date] = health.ComputeWalkingHREligibility(r)
	}

	written := 0
	var firstErr error
	for d := fromT; !d.After(toT); d = d.AddDate(0, 0, 1) {
		date := d.Format(isoDate)
		if err := s.writePassiveEfficiencyRow(context.Background(), d, date, rowByDate, verdictByDate); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		written++
	}
	return written, firstErr
}

func (s *DB) writePassiveEfficiencyRow(
	ctx context.Context,
	t time.Time,
	date string,
	rowByDate map[string]health.WalkingHRRow,
	verdict map[string]health.WalkingHREligibilityResult,
) error {
	_ = ctx
	epoch, err := s.ResolveSourceEpoch(date)
	if err != nil {
		return fmt.Errorf("resolve source_epoch for %s: %w", date, err)
	}
	epochStart := s.lookupEpochStart(epoch)

	// daily_point target — eligibility verdict for t+1.
	tp1 := t.AddDate(0, 0, 1).Format(isoDate)
	dailySpec := passiveTargetFromVerdict(tp1, verdict, rowByDate)

	// rolling_3d target — mean of eligible values across t+1..t+3.
	// Same all-or-nothing rule as Recovery to avoid silent bias from
	// partial averages.
	rollingSpec := passiveRolling3dTarget(t, verdict, rowByDate)

	// Features ≤ end of day t.
	features := buildPassiveEfficiencyFeatures(t, epochStart, verdict)
	featuresJSON, err := json.Marshal(features)
	if err != nil {
		return fmt.Errorf("marshal features %s: %w", date, err)
	}

	// Naive baselines for both target_kinds.
	baselines := buildPassiveEfficiencyNaiveBaselines(t, epochStart, verdict)

	if err := s.SaveTargetSnapshot(TargetSnapshot{
		Date:              date,
		SubScore:          SubScorePassiveEfficiency,
		TargetKind:        TargetKindDailyPoint,
		TargetValue:       dailySpec.Value,
		Eligible:          dailySpec.Eligible,
		EligibilityReason: dailySpec.Reason,
		DataCoverage:      dailySpec.Coverage,
		SourceEpoch:       epoch,
		FormulaVersion:    passiveEfficiencyFormulaVersion,
	}); err != nil {
		return fmt.Errorf("save daily_point target %s: %w", date, err)
	}
	if err := s.SaveTargetSnapshot(TargetSnapshot{
		Date:              date,
		SubScore:          SubScorePassiveEfficiency,
		TargetKind:        TargetKindRolling3d,
		TargetValue:       rollingSpec.Value,
		Eligible:          rollingSpec.Eligible,
		EligibilityReason: rollingSpec.Reason,
		DataCoverage:      rollingSpec.Coverage,
		SourceEpoch:       epoch,
		FormulaVersion:    passiveEfficiencyFormulaVersion,
	}); err != nil {
		return fmt.Errorf("save rolling_3d target %s: %w", date, err)
	}
	if err := s.SaveFeatureSnapshot(FeatureSnapshot{
		Date:           date,
		SubScore:       SubScorePassiveEfficiency,
		Features:       featuresJSON,
		SourceEpoch:    epoch,
		FeatureVersion: passiveEfficiencyFeatureVersion,
	}); err != nil {
		return fmt.Errorf("save features %s: %w", date, err)
	}
	for _, b := range baselines {
		nb := NaiveBaseline{
			Date:           date,
			SubScore:       SubScorePassiveEfficiency,
			TargetKind:     b.TargetKind,
			BaselineKind:   b.BaselineKind,
			PredictedValue: b.Value,
			Reason:         b.Reason,
			SourceEpoch:    epoch,
			FormulaVersion: passiveEfficiencyFormulaVersion,
		}
		if err := s.SaveNaiveBaseline(nb); err != nil {
			return fmt.Errorf("save baseline %s/%s/%s: %w", date, b.TargetKind, b.BaselineKind, err)
		}
	}
	return nil
}

func passiveTargetFromVerdict(
	targetDate string,
	verdict map[string]health.WalkingHREligibilityResult,
	rowByDate map[string]health.WalkingHRRow,
) targetWriteSpec {
	row, hasRow := rowByDate[targetDate]
	v, hasVerdict := verdict[targetDate]
	if !hasVerdict {
		// Date entirely absent from the loaded series — no metric_points
		// rows. Distinct from `walking_hr_out_of_range`.
		return targetWriteSpec{
			Eligible: false,
			Reason:   health.PassiveEfficiencyNoWalkingHR,
			Coverage: mustMarshal(map[string]any{
				"target_date": targetDate,
				"reason_detail": "no walking_heart_rate_average row for target date",
			}),
		}
	}
	cov := mustMarshal(map[string]any{
		"target_date":   targetDate,
		"observed_bpm":  row.Value,
		"has_row":       hasRow,
		"reason_detail": v.EligibilityReason,
	})
	return targetWriteSpec{
		Value:    v.Value,
		Eligible: v.Eligible,
		Reason:   v.EligibilityReason,
		Coverage: cov,
	}
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

func rowValueOrNil(rowByDate map[string]health.WalkingHRRow, date string) *float64 {
	r, ok := rowByDate[date]
	if !ok {
		return nil
	}
	return r.Value
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

// --- Features ----------------------------------------------------------

type passiveEfficiencyFeatures struct {
	PrevWalkingHR     *float64 `json:"prev_walking_hr,omitempty"`
	Mean7d            *float64 `json:"walking_hr_mean_7d,omitempty"`
	Mean30d           *float64 `json:"walking_hr_mean_30d,omitempty"`
	EWMA45            *float64 `json:"walking_hr_ewma_45d,omitempty"`
	EWMA180           *float64 `json:"walking_hr_ewma_180d,omitempty"`
	DeltaVsEWMA45     *float64 `json:"walking_hr_delta_vs_ewma_45d,omitempty"`
	DeltaVsEWMA180    *float64 `json:"walking_hr_delta_vs_ewma_180d,omitempty"`
	EligibleCount7d   int      `json:"eligible_count_7d"`
	EligibleCount45d  int      `json:"eligible_count_45d"`
	EligibleCount180d int      `json:"eligible_count_180d"`
	WarmupComplete45  bool     `json:"warmup_complete_45d"`
	WarmupComplete180 bool     `json:"warmup_complete_180d"`
}

func walkingHRLookup(verdict map[string]health.WalkingHREligibilityResult) DailyValueLookup {
	return func(date string) (*float64, bool) {
		v, ok := verdict[date]
		if !ok || !v.Eligible || v.Value == nil {
			return nil, false
		}
		return v.Value, true
	}
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
