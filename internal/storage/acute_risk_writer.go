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
	if err := validateReadinessDateRange(from, to, "BackfillAcuteRiskSnapshots"); err != nil {
		return 0, err
	}
	epochs, err := s.loadSourceEpochRunCache()
	if err != nil {
		return 0, err
	}
	return s.backfillAcuteRiskSnapshots(from, to, epochs)
}

func (s *DB) backfillAcuteRiskSnapshots(from, to string, epochs *sourceEpochRunCache) (int, error) {
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
		if err := s.writeAcuteRiskRow(context.Background(), d, date, epochs, rowByDate, hrvLookup, rhrLookup, orEventByDate, strictEventByDate); err != nil {
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
	epochs *sourceEpochRunCache,
	rowByDate map[string]health.AutonomicRow,
	hrvLookup, rhrLookup DailyValueLookup,
	orEventByDate, strictEventByDate map[string]int,
) error {
	epoch, epochStart := epochs.resolve(date)

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
			"paired_count":      pairedCount,
			"warmup_min_paired": health.AcuteRiskWarmupMinPaired,
			"reason_detail":     "paired HRV+RHR count below warmup threshold within current source_epoch",
		})
		labels, err := s.chipTargets(ctx, chipTargetWindow{date: date, subScore: SubScoreAcuteRisk, paired: pairedCount}, TargetKindEventT1T3, TargetKindEventStrictT1T3)
		if err != nil {
			return err
		}
		for _, tk := range []string{TargetKindEventT1T3, TargetKindEventStrictT1T3} {
			if err := s.saveChipTarget(date, SubScoreAcuteRisk, tk, epoch, acuteRiskFormulaVersion, labels[tk], cov); err != nil {
				return fmt.Errorf("save warmup-ineligible %s/%s: %w", date, tk, err)
			}
		}
		// Still emit a feature snapshot so the framework contract is
		// consistent (1 feature row per (date, sub_score)).
		if err := s.saveAcuteRiskFeatures(ctx, date, t, epoch, epochStart, hrvLookup, rhrLookup, pairedCount, false); err != nil {
			return err
		}
		// Baselines write even when paired-warmup hasn't been met. The
		// `event_base_rate` looks at prior labels strictly before t
		// and is independent of HRV/RHR pair availability for this
		// epoch. On day-zero of a fresh epoch this yields
		// (NULL, baseline_source_epoch_boundary) — operator sees the
		// pre-warmup state instead of a blank row.
		if err := s.saveEventBaseRateBaseline(ctx,
			t, date, SubScoreAcuteRisk, TargetKindEventT1T3,
			orEventByDate, epoch, epochStart, acuteRiskFormulaVersion); err != nil {
			return fmt.Errorf("save warmup-ineligible baseline %s/event_t1_t3: %w", date, err)
		}
		if err := s.saveEventBaseRateBaseline(ctx,
			t, date, SubScoreAcuteRisk, TargetKindEventStrictT1T3,
			strictEventByDate, epoch, epochStart, acuteRiskFormulaVersion); err != nil {
			return fmt.Errorf("save warmup-ineligible baseline %s/event_strict_t1_t3: %w", date, err)
		}
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
		labels, err := s.chipTargets(ctx, chipTargetWindow{date: date, subScore: SubScoreAcuteRisk, paired: pairedCount}, TargetKindEventT1T3, TargetKindEventStrictT1T3)
		if err != nil {
			return err
		}
		for _, tk := range []string{TargetKindEventT1T3, TargetKindEventStrictT1T3} {
			if err := s.saveChipTarget(date, SubScoreAcuteRisk, tk, epoch, acuteRiskFormulaVersion, labels[tk], cov); err != nil {
				return fmt.Errorf("save window-missing %s/%s: %w", date, tk, err)
			}
		}
		// Baselines write regardless of forward-window observability.
		// `event_base_rate` is computed from prior labels strictly
		// before t — it does not depend on whether t+1..t+3 is closed.
		// Without this, the chip on the bleeding edge of every backfill
		// (the last 3 dates) renders as `pending` forever, hiding a
		// deployable prediction the user could otherwise see.
		if err := s.saveEventBaseRateBaseline(ctx,
			t, date, SubScoreAcuteRisk, TargetKindEventT1T3,
			orEventByDate, epoch, epochStart, acuteRiskFormulaVersion); err != nil {
			return fmt.Errorf("save window-missing baseline %s/event_t1_t3: %w", date, err)
		}
		if err := s.saveEventBaseRateBaseline(ctx,
			t, date, SubScoreAcuteRisk, TargetKindEventStrictT1T3,
			strictEventByDate, epoch, epochStart, acuteRiskFormulaVersion); err != nil {
			return fmt.Errorf("save window-missing baseline %s/event_strict_t1_t3: %w", date, err)
		}
		// Feature snapshot still emitted so the (date, sub_score) PK is
		// covered. warmupMet=true because the gate did pass; the window
		// gate fired downstream of warmup.
		return s.saveAcuteRiskFeatures(ctx, date, t, epoch, epochStart, hrvLookup, rhrLookup, pairedCount, true)
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
		dayResults = append(dayResults, db)
	}

	labels, err := s.chipTargets(ctx, chipTargetWindow{date: date, subScore: SubScoreAcuteRisk, paired: pairedCount, days: dayResults}, TargetKindEventT1T3, TargetKindEventStrictT1T3)
	if err != nil {
		return err
	}

	cov := mustMarshal(map[string]any{
		"window_dates": []string{
			candidates[0].Format(isoDate),
			candidates[1].Format(isoDate),
			candidates[2].Format(isoDate),
		},
		"per_day":         dayResults,
		"paired_count":    pairedCount,
		"hrv_z_threshold": health.AcuteRiskHRVZThreshold,
		"rhr_z_threshold": health.AcuteRiskRHRZThreshold,
		"baseline_window": health.AcuteRiskBaselineWindowDays,
	})

	for _, tk := range []string{TargetKindEventT1T3, TargetKindEventStrictT1T3} {
		if err := s.saveChipTarget(date, SubScoreAcuteRisk, tk, epoch, acuteRiskFormulaVersion, labels[tk], cov); err != nil {
			return err
		}
	}
	if v := labels[TargetKindEventT1T3].Value; v != nil {
		orEventByDate[date] = int(*v)
	}
	if v := labels[TargetKindEventStrictT1T3].Value; v != nil {
		strictEventByDate[date] = int(*v)
	}

	// Naive event base rates over the prior 90 days, computed
	// independently per target_kind from its own label history. Same
	// helper as the event_window_data_missing branch — see
	// saveEventBaseRateBaseline's docstring for the offset/reason rule.
	if err := s.saveEventBaseRateBaseline(ctx,
		t, date, SubScoreAcuteRisk, TargetKindEventT1T3,
		orEventByDate, epoch, epochStart, acuteRiskFormulaVersion); err != nil {
		return fmt.Errorf("save base-rate baseline %s/event_t1_t3: %w", date, err)
	}
	if err := s.saveEventBaseRateBaseline(ctx,
		t, date, SubScoreAcuteRisk, TargetKindEventStrictT1T3,
		strictEventByDate, epoch, epochStart, acuteRiskFormulaVersion); err != nil {
		return fmt.Errorf("save base-rate baseline %s/event_strict_t1_t3: %w", date, err)
	}

	return s.saveAcuteRiskFeatures(ctx, date, t, epoch, epochStart, hrvLookup, rhrLookup, pairedCount, true)
}

// --- Features ----------------------------------------------------------

type acuteRiskFeatures struct {
	HRVToday           *float64 `json:"hrv_today,omitempty"`
	RHRToday           *float64 `json:"rhr_today,omitempty"`
	HRVMean45          *float64 `json:"hrv_mean_45d,omitempty"`
	HRVSD45            *float64 `json:"hrv_sd_45d,omitempty"`
	RHRMean45          *float64 `json:"rhr_mean_45d,omitempty"`
	RHRSD45            *float64 `json:"rhr_sd_45d,omitempty"`
	HRVZToday          *float64 `json:"hrv_z_today,omitempty"`
	RHRZToday          *float64 `json:"rhr_z_today,omitempty"`
	PairedCountToT     int      `json:"paired_count_to_t"`
	WarmupMet          bool     `json:"warmup_met"`
	HRVEligibleCount45 int      `json:"hrv_eligible_count_45d"`
	RHREligibleCount45 int      `json:"rhr_eligible_count_45d"`
}

func (s *DB) saveAcuteRiskFeatures(
	ctx context.Context,
	date string,
	t time.Time,
	epoch string,
	epochStart string,
	hrvLookup, rhrLookup DailyValueLookup,
	pairedCount int,
	warmupMet bool,
) error {
	feat := acuteRiskFeatures{
		PairedCountToT: pairedCount,
		WarmupMet:      warmupMet,
	}
	hrv, err := s.chipFeatureWindow(ctx, t, epochStart, health.AcuteRiskBaselineWindowDays-1, hrvLookup)
	if err != nil {
		return err
	}
	rhr, err := s.chipFeatureWindow(ctx, t, epochStart, health.AcuteRiskBaselineWindowDays-1, rhrLookup)
	if err != nil {
		return err
	}
	feat.HRVToday, feat.HRVMean45, feat.HRVSD45, feat.HRVZToday, feat.HRVEligibleCount45 = hrv.Today, hrv.Mean45, hrv.SD45, hrv.Z45, hrv.N45
	feat.RHRToday, feat.RHRMean45, feat.RHRSD45, feat.RHRZToday, feat.RHREligibleCount45 = rhr.Today, rhr.Mean45, rhr.SD45, rhr.Z45, rhr.N45
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

// saveEventBaseRateBaseline writes one event_base_rate naive_baselines
// row for the given (date, sub_score, target_kind), computing the rate
// from `eventByDate` over the 90 prior days. Centralised because the
// baseline is independent of the forward target window — every writer
// branch (eligible target, event_window_data_missing, warmup not met)
// emits one of these rows so the chip can render even on the bleeding
// edge of the backfill where the future label window hasn't closed
// yet.
//
// The dbt baseline model returns the epoch-boundary or warmup reason when the
// preceding window contains no eligible labels.
func (s *DB) saveEventBaseRateBaseline(
	ctx context.Context,
	t time.Time,
	date string,
	subScore, targetKind string,
	eventByDate map[string]int,
	epoch, epochStart string,
	formulaVersion int,
) error {
	rows, err := s.chipBaselines(ctx, t, epochStart, subScore, func(day string) (*float64, bool) {
		label, ok := eventByDate[day]
		value := float64(label)
		return &value, ok
	}, targetKind)
	if err != nil {
		return err
	}
	if len(rows) != 1 || rows[0].BaselineKind != BaselineKindEventBaseRate {
		return fmt.Errorf("unexpected event baselines for %s", subScore)
	}
	rate, reason := rows[0].Value, rows[0].Reason
	return s.SaveNaiveBaseline(NaiveBaseline{
		Date:           date,
		SubScore:       subScore,
		TargetKind:     targetKind,
		BaselineKind:   BaselineKindEventBaseRate,
		PredictedValue: rate,
		Reason:         reason,
		SourceEpoch:    epoch,
		FormulaVersion: formulaVersion,
	})
}
