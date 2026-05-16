// Package storage — Acute Risk writer.
//
// Third sub-score writer (plan §4.2). Target is a binary event over
// the t+1..t+3 window: did HRV or RHR breach personal baseline by ≥
// 1.5σ on any of those three days? The OR-form is the primary label
// (`event_t1_t3`); the AND-form ("same day both channels breached")
// is the strict secondary label (`event_strict_t1_t3`).
//
// Methodologically critical:
//
//   - Each candidate day `d ∈ {t+1, t+2, t+3}` is scored against the
//     baseline computed from its OWN history (days strictly before d),
//     not against a single baseline frozen at `t`. Otherwise the
//     label leaks: `d` itself influences the threshold it must beat.
//     `windowStatsBefore` in sub_score_window_helpers.go enforces the
//     exclusion.
//
//   - Eligibility gates on PAIRED warmup count (HRV+RHR both present
//     on same day) inside the current source_epoch, before t+1.
//     Below AcuteRiskWarmupMinPaired the row is `baseline_warmup`
//     ineligible. Above, both target_kind rows write the label.
//
//   - The two target rows have distinct PKs
//     (target_kind = event_t1_t3 vs event_strict_t1_t3) so the strict
//     variant cannot overwrite the primary.
//
//   - This writer runs in cold-start silent mode per plan §3.5: labels
//     accumulate in the DB but no UI consumes them until calibration
//     of precision@recall is stable across two consecutive quarters.

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"health-receiver/internal/health"
)

const acuteRiskFormulaVersion = 1
const acuteRiskFeatureVersion = 1

// LoadAutonomicRows pulls hrv_avg, rhr_avg from daily_scores for the
// inclusive date range. Rows are returned in ascending date order;
// missing dates omitted (writer treats them as no observation).
func (s *DB) LoadAutonomicRows(from, to string) ([]health.AutonomicRow, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT date, hrv_avg, rhr_avg
		  FROM daily_scores
		 WHERE date BETWEEN $1 AND $2
		 ORDER BY date ASC
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("LoadAutonomicRows: %w", err)
	}
	defer rows.Close()
	var out []health.AutonomicRow
	for rows.Next() {
		var r health.AutonomicRow
		var hrv, rhr *float32
		if err := rows.Scan(&r.Date, &hrv, &rhr); err != nil {
			return nil, fmt.Errorf("LoadAutonomicRows scan: %w", err)
		}
		r.HRV = liftFloat(hrv)
		r.RHR = liftFloat(rhr)
		out = append(out, r)
	}
	return out, rows.Err()
}

// BackfillAcuteRiskSnapshots writes event-label target snapshots,
// feature snapshots, and a naive event-base-rate baseline for the
// Acute Risk sub-score across [from, to]. Idempotent.
//
// Load window: 180 days before `from` for warmup + 3 days after `to`
// so the t+1..t+3 window is fully observable.
func (s *DB) BackfillAcuteRiskSnapshots(from, to string) (int, error) {
	fromT, err := time.Parse(isoDate, from)
	if err != nil {
		return 0, fmt.Errorf("BackfillAcuteRiskSnapshots: parse from: %w", err)
	}
	toT, err := time.Parse(isoDate, to)
	if err != nil {
		return 0, fmt.Errorf("BackfillAcuteRiskSnapshots: parse to: %w", err)
	}
	if toT.Before(fromT) {
		return 0, fmt.Errorf("BackfillAcuteRiskSnapshots: to %q before from %q", to, from)
	}

	loadFrom := fromT.AddDate(0, 0, -ewmaWindowSlow).Format(isoDate)
	loadTo := toT.AddDate(0, 0, 3).Format(isoDate)
	loaded, err := s.LoadAutonomicRows(loadFrom, loadTo)
	if err != nil {
		return 0, err
	}

	rowByDate := make(map[string]health.AutonomicRow, len(loaded))
	for _, r := range loaded {
		rowByDate[r.Date] = r
	}
	hrvLookup := autonomicHRVLookup(rowByDate)
	rhrLookup := autonomicRHRLookup(rowByDate)

	// Pre-load prior event labels (BOTH or-event and strict-event) in
	// the (from-90, from-1) range so the naive event_base_rate baselines
	// have history to average over for the first dates in the run.
	// Strict labels must be loaded separately because the strict event
	// distribution is sparser than the OR event distribution; using OR
	// labels to compute a "strict base rate" would systematically
	// overestimate strict frequency (Codex review comment, PR #93).
	orEventByDate, strictEventByDate, err := s.loadPriorEventLabels(fromT)
	if err != nil {
		return 0, err
	}

	written := 0
	var firstErr error
	for d := fromT; !d.After(toT); d = d.AddDate(0, 0, 1) {
		date := d.Format(isoDate)
		if err := s.writeAcuteRiskRow(context.Background(), d, date, rowByDate, hrvLookup, rhrLookup, orEventByDate, strictEventByDate); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		written++
	}
	return written, firstErr
}

func (s *DB) writeAcuteRiskRow(
	ctx context.Context,
	t time.Time,
	date string,
	rowByDate map[string]health.AutonomicRow,
	hrvLookup, rhrLookup DailyValueLookup,
	orEventByDate, strictEventByDate map[string]int,
) error {
	_ = ctx
	epoch, err := s.ResolveSourceEpoch(date)
	if err != nil {
		return fmt.Errorf("resolve source_epoch for %s: %w", date, err)
	}
	epochStart := s.lookupEpochStart(epoch)

	// Paired warmup count: days in (epochStart, t] with both HRV and
	// RHR present. Note we count *up to and including* t — that's the
	// data the writer would have access to at the moment of evaluating
	// the t+1..t+3 window.
	pairedCount := countPairedObservations(t, epochStart, rowByDate)

	if pairedCount < health.AcuteRiskWarmupMinPaired {
		// Ineligible: not enough history to set thresholds. Both
		// target_kinds get the same reason; PKs distinct so the strict
		// row does not overwrite the primary.
		cov := mustMarshal(map[string]any{
			"paired_count":       pairedCount,
			"warmup_min_paired":  health.AcuteRiskWarmupMinPaired,
			"reason_detail":      "paired HRV+RHR count below warmup threshold within current source_epoch",
		})
		for _, tk := range []string{TargetKindEventT1T3, TargetKindEventStrictT1T3} {
			if err := s.SaveTargetSnapshot(TargetSnapshot{
				Date:              date,
				SubScore:          SubScoreAcuteRisk,
				TargetKind:        tk,
				Eligible:          false,
				EligibilityReason: health.AcuteRiskEligibilityBaselineWarmup,
				DataCoverage:      cov,
				SourceEpoch:       epoch,
				FormulaVersion:    acuteRiskFormulaVersion,
			}); err != nil {
				return fmt.Errorf("save warmup-ineligible %s/%s: %w", date, tk, err)
			}
		}
		// Still emit a feature snapshot so the framework contract is
		// consistent (1 feature row per (date, sub_score)).
		if err := s.saveAcuteRiskFeatures(date, t, epoch, epochStart, rowByDate, hrvLookup, rhrLookup, pairedCount, false); err != nil {
			return err
		}
		// No naive baseline written for ineligible — predicted_value
		// has no meaningful target to predict.
		return nil
	}

	// Evaluate each candidate day in t+1..t+3 against its own prior
	// baseline (windowStatsBefore excludes the candidate itself).
	candidates := []time.Time{
		t.AddDate(0, 0, 1),
		t.AddDate(0, 0, 2),
		t.AddDate(0, 0, 3),
	}

	// Window-observability gate: every day in the window must have at
	// least one observable channel (HRV or RHR non-nil) before we can
	// honestly emit a negative label. A fully-missing day could have
	// hidden a breach we'd then silently miscode as event=0. The
	// bleeding edge of every backfill (the last 3 dates) and any
	// intra-window sensor gap hit this gate. Codex review comment, PR #93.
	var missingWindowDays []string
	for _, c := range candidates {
		ds := c.Format(isoDate)
		row, present := rowByDate[ds]
		if !present || (row.HRV == nil && row.RHR == nil) {
			missingWindowDays = append(missingWindowDays, ds)
		}
	}
	if len(missingWindowDays) > 0 {
		cov := mustMarshal(map[string]any{
			"window_dates": []string{
				candidates[0].Format(isoDate),
				candidates[1].Format(isoDate),
				candidates[2].Format(isoDate),
			},
			"missing_window_days": missingWindowDays,
			"paired_count":        pairedCount,
			"reason_detail":       "at least one day in t+1..t+3 has no observable autonomic signal",
		})
		for _, tk := range []string{TargetKindEventT1T3, TargetKindEventStrictT1T3} {
			if err := s.SaveTargetSnapshot(TargetSnapshot{
				Date:              date,
				SubScore:          SubScoreAcuteRisk,
				TargetKind:        tk,
				Eligible:          false,
				EligibilityReason: health.AcuteRiskEligibilityEventWindowDataMissing,
				DataCoverage:      cov,
				SourceEpoch:       epoch,
				FormulaVersion:    acuteRiskFormulaVersion,
			}); err != nil {
				return fmt.Errorf("save window-missing %s/%s: %w", date, tk, err)
			}
		}
		// Feature snapshot still emitted so the (date, sub_score) PK is
		// covered. warmupMet=true because the gate did pass; the window
		// gate fired downstream of warmup.
		return s.saveAcuteRiskFeatures(date, t, epoch, epochStart, rowByDate, hrvLookup, rhrLookup, pairedCount, true)
	}

	type dayBreach struct {
		Date     string   `json:"date"`
		HRVValue *float64 `json:"hrv,omitempty"`
		HRVZ     *float64 `json:"hrv_z,omitempty"`
		RHRValue *float64 `json:"rhr,omitempty"`
		RHRZ     *float64 `json:"rhr_z,omitempty"`
		HRVDrop  bool     `json:"hrv_drop"`
		RHRSpike bool     `json:"rhr_spike"`
		Strict   bool     `json:"strict"`
	}
	dayResults := make([]dayBreach, 0, 3)
	var eventOR, eventStrict bool

	for _, c := range candidates {
		ds := c.Format(isoDate)
		row := rowByDate[ds]
		db := dayBreach{Date: ds}

		hrvMean, hrvSD, _ := windowStatsBefore(c, health.AcuteRiskBaselineWindowDays, epochStart, hrvLookup)
		rhrMean, rhrSD, _ := windowStatsBefore(c, health.AcuteRiskBaselineWindowDays, epochStart, rhrLookup)

		if row.HRV != nil {
			db.HRVValue = ptrFloat(*row.HRV)
			if z := zScoreOrNil(*row.HRV, hrvMean, hrvSD); z != nil {
				db.HRVZ = z
				if *z <= health.AcuteRiskHRVZThreshold {
					db.HRVDrop = true
				}
			}
		}
		if row.RHR != nil {
			db.RHRValue = ptrFloat(*row.RHR)
			if z := zScoreOrNil(*row.RHR, rhrMean, rhrSD); z != nil {
				db.RHRZ = z
				if *z >= health.AcuteRiskRHRZThreshold {
					db.RHRSpike = true
				}
			}
		}
		db.Strict = db.HRVDrop && db.RHRSpike
		if db.HRVDrop || db.RHRSpike {
			eventOR = true
		}
		if db.Strict {
			eventStrict = true
		}
		dayResults = append(dayResults, db)
	}

	orVal := boolToFloat(eventOR)
	strictVal := boolToFloat(eventStrict)

	cov := mustMarshal(map[string]any{
		"window_dates": []string{
			candidates[0].Format(isoDate),
			candidates[1].Format(isoDate),
			candidates[2].Format(isoDate),
		},
		"per_day":            dayResults,
		"paired_count":       pairedCount,
		"hrv_z_threshold":    health.AcuteRiskHRVZThreshold,
		"rhr_z_threshold":    health.AcuteRiskRHRZThreshold,
		"baseline_window":    health.AcuteRiskBaselineWindowDays,
	})

	// Primary OR-event.
	if err := s.SaveTargetSnapshot(TargetSnapshot{
		Date:              date,
		SubScore:          SubScoreAcuteRisk,
		TargetKind:        TargetKindEventT1T3,
		TargetValue:       &orVal,
		Eligible:          true,
		EligibilityReason: health.AcuteRiskEligibilityOK,
		DataCoverage:      cov,
		SourceEpoch:       epoch,
		FormulaVersion:    acuteRiskFormulaVersion,
	}); err != nil {
		return fmt.Errorf("save event_t1_t3 %s: %w", date, err)
	}
	// Secondary strict AND-event. Distinct target_kind, distinct PK —
	// cannot overwrite primary.
	if err := s.SaveTargetSnapshot(TargetSnapshot{
		Date:              date,
		SubScore:          SubScoreAcuteRisk,
		TargetKind:        TargetKindEventStrictT1T3,
		TargetValue:       &strictVal,
		Eligible:          true,
		EligibilityReason: health.AcuteRiskEligibilityOK,
		DataCoverage:      cov,
		SourceEpoch:       epoch,
		FormulaVersion:    acuteRiskFormulaVersion,
	}); err != nil {
		return fmt.Errorf("save event_strict_t1_t3 %s: %w", date, err)
	}

	// Remember this date's labels (both OR and strict) so the in-memory
	// base-rate baselines for later dates in the same run can see them.
	// Two maps — strict baseline must NOT be computed from OR labels
	// because strict events are sparser (Codex review comment, PR #93).
	orEventByDate[date] = int(orVal)
	strictEventByDate[date] = int(strictVal)

	// Naive event base rates over the prior 90 days, computed
	// independently per target_kind from its own label history. Reason
	// uses the same 90-day window passed into priorEventBaseRate so
	// the source_epoch_boundary check matches what the baseline
	// actually looked at.
	orBaseRate := priorEventBaseRate(t, 90, orEventByDate)
	strictBaseRate := priorEventBaseRate(t, 90, strictEventByDate)
	nullReason := classifyBaselineNullReason(t, 90, epochStart)
	for _, b := range []struct {
		tk   string
		rate *float64
	}{
		{TargetKindEventT1T3, orBaseRate},
		{TargetKindEventStrictT1T3, strictBaseRate},
	} {
		reason := ""
		if b.rate == nil {
			reason = nullReason
		}
		if err := s.SaveNaiveBaseline(NaiveBaseline{
			Date:           date,
			SubScore:       SubScoreAcuteRisk,
			TargetKind:     b.tk,
			BaselineKind:   BaselineKindEventBaseRate,
			PredictedValue: b.rate,
			Reason:         reason,
			SourceEpoch:    epoch,
			FormulaVersion: acuteRiskFormulaVersion,
		}); err != nil {
			return fmt.Errorf("save base-rate baseline %s/%s: %w", date, b.tk, err)
		}
	}

	return s.saveAcuteRiskFeatures(date, t, epoch, epochStart, rowByDate, hrvLookup, rhrLookup, pairedCount, true)
}

// --- Features ----------------------------------------------------------

type acuteRiskFeatures struct {
	HRVToday          *float64 `json:"hrv_today,omitempty"`
	RHRToday          *float64 `json:"rhr_today,omitempty"`
	HRVMean45         *float64 `json:"hrv_mean_45d,omitempty"`
	HRVSD45           *float64 `json:"hrv_sd_45d,omitempty"`
	RHRMean45         *float64 `json:"rhr_mean_45d,omitempty"`
	RHRSD45           *float64 `json:"rhr_sd_45d,omitempty"`
	HRVZToday         *float64 `json:"hrv_z_today,omitempty"`
	RHRZToday         *float64 `json:"rhr_z_today,omitempty"`
	PairedCountToT    int      `json:"paired_count_to_t"`
	WarmupMet         bool     `json:"warmup_met"`
	HRVEligibleCount45 int     `json:"hrv_eligible_count_45d"`
	RHREligibleCount45 int     `json:"rhr_eligible_count_45d"`
}

func (s *DB) saveAcuteRiskFeatures(
	date string,
	t time.Time,
	epoch string,
	epochStart string,
	rowByDate map[string]health.AutonomicRow,
	hrvLookup, rhrLookup DailyValueLookup,
	pairedCount int,
	warmupMet bool,
) error {
	feat := acuteRiskFeatures{
		PairedCountToT: pairedCount,
		WarmupMet:      warmupMet,
	}
	if row, ok := rowByDate[t.Format(isoDate)]; ok {
		if row.HRV != nil {
			feat.HRVToday = ptrFloat(*row.HRV)
		}
		if row.RHR != nil {
			feat.RHRToday = ptrFloat(*row.RHR)
		}
	}

	// Baselines at end-of-day-t (inclusive). Distinct from the
	// per-candidate baselines used for target labelling (which exclude
	// the candidate). Both are honest features — they reflect state
	// available at t.
	tPlusOne := t.AddDate(0, 0, 1)
	hrvMean, hrvSD, hrvN := windowStatsBefore(tPlusOne, health.AcuteRiskBaselineWindowDays, epochStart, hrvLookup)
	rhrMean, rhrSD, rhrN := windowStatsBefore(tPlusOne, health.AcuteRiskBaselineWindowDays, epochStart, rhrLookup)
	feat.HRVMean45 = hrvMean
	feat.HRVSD45 = hrvSD
	feat.RHRMean45 = rhrMean
	feat.RHRSD45 = rhrSD
	feat.HRVEligibleCount45 = hrvN
	feat.RHREligibleCount45 = rhrN
	if feat.HRVToday != nil {
		feat.HRVZToday = zScoreOrNil(*feat.HRVToday, hrvMean, hrvSD)
	}
	if feat.RHRToday != nil {
		feat.RHRZToday = zScoreOrNil(*feat.RHRToday, rhrMean, rhrSD)
	}

	featuresJSON, err := json.Marshal(feat)
	if err != nil {
		return fmt.Errorf("marshal acute features %s: %w", date, err)
	}
	return s.SaveFeatureSnapshot(FeatureSnapshot{
		Date:           date,
		SubScore:       SubScoreAcuteRisk,
		Features:       featuresJSON,
		SourceEpoch:    epoch,
		FeatureVersion: acuteRiskFeatureVersion,
	})
}

// --- Helpers -----------------------------------------------------------

// autonomicHRVLookup adapts the per-date HRV view of an AutonomicRow
// map to the DailyValueLookup contract used by windowStatsBefore.
func autonomicHRVLookup(rows map[string]health.AutonomicRow) DailyValueLookup {
	return func(date string) (*float64, bool) {
		r, ok := rows[date]
		if !ok || r.HRV == nil {
			return nil, false
		}
		return r.HRV, true
	}
}

func autonomicRHRLookup(rows map[string]health.AutonomicRow) DailyValueLookup {
	return func(date string) (*float64, bool) {
		r, ok := rows[date]
		if !ok || r.RHR == nil {
			return nil, false
		}
		return r.RHR, true
	}
}

// countPairedObservations counts days with BOTH HRV and RHR non-NULL
// in the trailing 180 days from `t` (inclusive), clipped at epochStart.
// 180d matches the slow baseline window; used as the warmup gate.
func countPairedObservations(t time.Time, epochStart string, rows map[string]health.AutonomicRow) int {
	var n int
	for i := 0; i <= ewmaWindowSlow; i++ {
		ds := t.AddDate(0, 0, -i).Format(isoDate)
		if epochStart != "" && ds < epochStart {
			continue
		}
		r, ok := rows[ds]
		if !ok || r.HRV == nil || r.RHR == nil {
			continue
		}
		n++
	}
	return n
}

// zScoreOrNil returns (value - mean) / sd, or nil if any input is
// nil/zero-SD. Guards against divide-by-zero on constant-history
// stretches.
func zScoreOrNil(value float64, mean, sd *float64) *float64 {
	if mean == nil || sd == nil || *sd <= 0 {
		return nil
	}
	z := (value - *mean) / *sd
	if math.IsNaN(z) || math.IsInf(z, 0) {
		return nil
	}
	return &z
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// loadPriorEventLabels reads previously-written OR-event and
// strict-event labels in the 90 days before `fromT` so the naive
// base-rate baselines for the first dates in a backfill have history
// to average over. Returns empty maps (not an error) when no rows
// exist — the in-run iteration then fills both maps starting from
// `fromT`.
//
// Loaded as a single query keyed on the target_kind column; the two
// maps are partitioned in Go so strict baseline cannot be computed
// from OR labels (Codex review comment, PR #93).
func (s *DB) loadPriorEventLabels(fromT time.Time) (orMap, strictMap map[string]int, err error) {
	ctx, cancel := queryCtx()
	defer cancel()
	priorFrom := fromT.AddDate(0, 0, -90).Format(isoDate)
	priorTo := fromT.AddDate(0, 0, -1).Format(isoDate)
	rows, qErr := s.pool.Query(ctx, `
		SELECT date, target_kind, target_value
		  FROM target_snapshots
		 WHERE sub_score = $1
		   AND target_kind IN ($2, $3)
		   AND eligible = TRUE
		   AND target_value IS NOT NULL
		   AND date BETWEEN $4 AND $5
	`, SubScoreAcuteRisk, TargetKindEventT1T3, TargetKindEventStrictT1T3, priorFrom, priorTo)
	if qErr != nil {
		return nil, nil, fmt.Errorf("loadPriorEventLabels: %w", qErr)
	}
	defer rows.Close()
	orMap = make(map[string]int)
	strictMap = make(map[string]int)
	for rows.Next() {
		var date, kind string
		var v float32
		if scanErr := rows.Scan(&date, &kind, &v); scanErr != nil {
			return nil, nil, fmt.Errorf("loadPriorEventLabels scan: %w", scanErr)
		}
		val := 0
		if v >= 0.5 {
			val = 1
		}
		switch kind {
		case TargetKindEventT1T3:
			orMap[date] = val
		case TargetKindEventStrictT1T3:
			strictMap[date] = val
		}
	}
	return orMap, strictMap, rows.Err()
}

// priorEventBaseRate averages the eligible event labels for the
// `windowDays` dates strictly before `t`. Returns nil when no eligible
// labels fall in the window (start of history before warmup completes).
func priorEventBaseRate(t time.Time, windowDays int, eventByDate map[string]int) *float64 {
	var sum, n int
	for i := 1; i <= windowDays; i++ {
		ds := t.AddDate(0, 0, -i).Format(isoDate)
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
