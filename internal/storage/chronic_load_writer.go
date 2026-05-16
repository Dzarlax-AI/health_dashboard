// Package storage — Chronic Load writer.
//
// Fourth and final Phase 0 sub-score writer (plan §4.2). Distinct
// from the earlier three because it does not compute physiology
// itself — it consumes the outputs of Recovery Stability (for the
// primary deterioration label) and Acute Risk (for the secondary
// acute-density label) from target_snapshots. Both upstream
// sub-scores must be backfilled over the same range before Chronic
// Load runs.
//
// Phase 0 boundary: emits labels + features only. No verdict logic,
// no UI consumer. Two distinct target_kinds:
//
//   chronic_label          — primary, binary 0/1. Set to 1 when
//                            Recovery 3d-roll efficiency on ≥5 of the
//                            14 forward days falls below its
//                            per-candidate-day EWMA45 baseline by
//                            more than 1σ. The per-candidate baseline
//                            uses windowStatsBefore so each day is
//                            scored against its own prior history,
//                            never against a baseline frozen at `t`.
//
//   chronic_acute_density  — secondary, binary 0/1. Set to 1 when
//                            the forward window contains ≥3 Acute
//                            Risk OR-events (event_t1_t3 = 1,
//                            eligible). Uses the primary Acute OR
//                            label, not strict — strict is too rare
//                            to make a meaningful density signal.

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"health-receiver/internal/health"
)

const chronicLoadFormulaVersion = 1
const chronicLoadFeatureVersion = 1

// recoveryRolling3dRow is the minimal Recovery rolling_3d view Chronic
// Load needs: target value (NULL when ineligible), eligibility flag,
// and the source_epoch the row was written under.
type recoveryRolling3dRow struct {
	Date        string
	Value       *float64
	Eligible    bool
	SourceEpoch string
}

// LoadRecoveryRolling3dRows reads Recovery's rolling_3d target rows
// across the inclusive date range. Returned in ascending date order.
func (s *DB) LoadRecoveryRolling3dRows(from, to string) ([]recoveryRolling3dRow, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT date, target_value, eligible, source_epoch
		  FROM target_snapshots
		 WHERE sub_score = $1
		   AND target_kind = $2
		   AND date BETWEEN $3 AND $4
		 ORDER BY date ASC
	`, SubScoreRecoveryStability, TargetKindRolling3d, from, to)
	if err != nil {
		return nil, fmt.Errorf("LoadRecoveryRolling3dRows: %w", err)
	}
	defer rows.Close()
	var out []recoveryRolling3dRow
	for rows.Next() {
		var r recoveryRolling3dRow
		var v *float32
		if err := rows.Scan(&r.Date, &v, &r.Eligible, &r.SourceEpoch); err != nil {
			return nil, fmt.Errorf("LoadRecoveryRolling3dRows scan: %w", err)
		}
		if v != nil {
			f := float64(*v)
			r.Value = &f
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LoadAcuteOrEventRows reads Acute Risk's event_t1_t3 target rows
// across the inclusive date range. Used by Chronic Load to count
// acute events in the forward window for the secondary label.
// Returned in ascending date order.
func (s *DB) LoadAcuteOrEventRows(from, to string) (map[string]int, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT date, target_value
		  FROM target_snapshots
		 WHERE sub_score = $1
		   AND target_kind = $2
		   AND eligible = TRUE
		   AND target_value IS NOT NULL
		   AND date BETWEEN $3 AND $4
		 ORDER BY date ASC
	`, SubScoreAcuteRisk, TargetKindEventT1T3, from, to)
	if err != nil {
		return nil, fmt.Errorf("LoadAcuteOrEventRows: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int)
	for rows.Next() {
		var date string
		var v float32
		if err := rows.Scan(&date, &v); err != nil {
			return nil, fmt.Errorf("LoadAcuteOrEventRows scan: %w", err)
		}
		if v >= 0.5 {
			out[date] = 1
		} else {
			out[date] = 0
		}
	}
	return out, rows.Err()
}

// BackfillChronicLoadSnapshots writes Chronic Load target snapshots
// (primary chronic_label + secondary chronic_acute_density), feature
// snapshots, and naive base-rate baselines for the inclusive date
// range. Idempotent on the PKs.
//
// Recovery and Acute Risk MUST be backfilled over the same range
// before Chronic Load — this writer reads their outputs. A missing
// upstream row collapses gracefully (forward-window count excludes
// the missing day) but warmup will fail if too many Recovery rows
// are ineligible/missing.
//
// Load window: 180 days before `from` for the EWMA45 baseline
// lookback + ChronicLoadForwardWindowDays after `to` for the forward
// window.
func (s *DB) BackfillChronicLoadSnapshots(from, to string) (int, error) {
	fromT, err := time.Parse(isoDate, from)
	if err != nil {
		return 0, fmt.Errorf("BackfillChronicLoadSnapshots: parse from: %w", err)
	}
	toT, err := time.Parse(isoDate, to)
	if err != nil {
		return 0, fmt.Errorf("BackfillChronicLoadSnapshots: parse to: %w", err)
	}
	if toT.Before(fromT) {
		return 0, fmt.Errorf("BackfillChronicLoadSnapshots: to %q before from %q", to, from)
	}

	loadFrom := fromT.AddDate(0, 0, -ewmaWindowSlow).Format(isoDate)
	loadTo := toT.AddDate(0, 0, health.ChronicLoadForwardWindowDays).Format(isoDate)

	recovery, err := s.LoadRecoveryRolling3dRows(loadFrom, loadTo)
	if err != nil {
		return 0, err
	}
	acute, err := s.LoadAcuteOrEventRows(loadFrom, loadTo)
	if err != nil {
		return 0, err
	}

	recoveryByDate := make(map[string]recoveryRolling3dRow, len(recovery))
	for _, r := range recovery {
		recoveryByDate[r.Date] = r
	}
	recoveryLookup := recoveryRolling3dLookup(recoveryByDate)

	// Pre-load prior Chronic labels (both primary + secondary) so the
	// naive base-rate baselines have history for the first dates in
	// the run. Same pattern as Acute Risk.
	priorChronic, priorAcuteDensity, err := s.loadPriorChronicLabels(fromT)
	if err != nil {
		return 0, err
	}

	written := 0
	var firstErr error
	for d := fromT; !d.After(toT); d = d.AddDate(0, 0, 1) {
		date := d.Format(isoDate)
		if err := s.writeChronicLoadRow(context.Background(), d, date,
			recoveryByDate, recoveryLookup, acute,
			priorChronic, priorAcuteDensity); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		written++
	}
	return written, firstErr
}

func (s *DB) writeChronicLoadRow(
	ctx context.Context,
	t time.Time,
	date string,
	recoveryByDate map[string]recoveryRolling3dRow,
	recoveryLookup DailyValueLookup,
	acuteOrByDate map[string]int,
	priorChronic, priorAcuteDensity map[string]int,
) error {
	_ = ctx
	epoch, err := s.ResolveSourceEpoch(date)
	if err != nil {
		return fmt.Errorf("resolve source_epoch for %s: %w", date, err)
	}
	epochStart := s.lookupEpochStart(epoch)

	// Paired warmup: count eligible Recovery rolling_3d rows strictly
	// before t+1, inside the current source_epoch. Counts up to and
	// including t — that's the data accessible at evaluation time.
	pairedCount := countEligibleRecoveryRows(t, epochStart, recoveryByDate)
	warmupMet := pairedCount >= health.ChronicLoadWarmupMinPaired

	// Feature snapshot is emitted in all cases (eligible or not) so the
	// (date, sub_score) PK contract holds. Build it once and reuse.
	features := buildChronicLoadFeatures(t, epochStart, recoveryByDate, recoveryLookup, pairedCount, warmupMet)
	featuresJSON, err := json.Marshal(features)
	if err != nil {
		return fmt.Errorf("marshal chronic features %s: %w", date, err)
	}

	if !warmupMet {
		cov := mustMarshal(map[string]any{
			"paired_count":      pairedCount,
			"warmup_min_paired": health.ChronicLoadWarmupMinPaired,
			"reason_detail":     "eligible Recovery rolling_3d count below warmup threshold within current source_epoch",
		})
		for _, tk := range []string{TargetKindChronicLabel, TargetKindChronicAcuteDensity} {
			if err := s.SaveTargetSnapshot(TargetSnapshot{
				Date:              date,
				SubScore:          SubScoreChronicLoad,
				TargetKind:        tk,
				Eligible:          false,
				EligibilityReason: health.ChronicLoadEligibilityBaselineWarmup,
				DataCoverage:      cov,
				SourceEpoch:       epoch,
				FormulaVersion:    chronicLoadFormulaVersion,
			}); err != nil {
				return fmt.Errorf("save warmup-ineligible %s/%s: %w", date, tk, err)
			}
		}
		return s.SaveFeatureSnapshot(FeatureSnapshot{
			Date:           date,
			SubScore:       SubScoreChronicLoad,
			Features:       featuresJSON,
			SourceEpoch:    epoch,
			FeatureVersion: chronicLoadFeatureVersion,
		})
	}

	// Evaluate forward window t+1..t+14. Each candidate day `d` is
	// scored against EWMA45 baseline computed from Recovery rolling_3d
	// values for dates strictly before `d` (windowStatsBefore excludes
	// the candidate). Missing days contribute nothing to the breach
	// count or the acute density count.
	type dayCell struct {
		Date       string   `json:"date"`
		Recovery   *float64 `json:"recovery_3d,omitempty"`
		BaselineM  *float64 `json:"baseline_mean,omitempty"`
		BaselineSD *float64 `json:"baseline_sd,omitempty"`
		Z          *float64 `json:"z,omitempty"`
		Breach     bool     `json:"breach"`
		AcuteOR    int      `json:"acute_or"`
	}
	cells := make([]dayCell, 0, health.ChronicLoadForwardWindowDays)
	var breachCount, acuteCount int

	for i := 1; i <= health.ChronicLoadForwardWindowDays; i++ {
		c := t.AddDate(0, 0, i)
		ds := c.Format(isoDate)
		cell := dayCell{Date: ds}

		// Acute density contribution.
		if v, ok := acuteOrByDate[ds]; ok {
			cell.AcuteOR = v
			if v == 1 {
				acuteCount++
			}
		}

		// Deterioration check.
		row, hasRow := recoveryByDate[ds]
		if !hasRow || !row.Eligible || row.Value == nil {
			cells = append(cells, cell)
			continue
		}
		cell.Recovery = ptrFloat(*row.Value)
		mean, sd, _ := windowStatsBefore(c, health.ChronicLoadBaselineWindowDays, epochStart, recoveryLookup)
		cell.BaselineM = mean
		cell.BaselineSD = sd
		if z := zScoreOrNil(*row.Value, mean, sd); z != nil {
			cell.Z = z
			if *z < health.ChronicLoadDeteriorationZThreshold {
				cell.Breach = true
				breachCount++
			}
		}
		cells = append(cells, cell)
	}

	chronicLabel := boolToFloat(breachCount >= health.ChronicLoadMinBreachDays)
	acuteDensityLabel := boolToFloat(acuteCount >= health.ChronicLoadMinAcuteDensity)

	cov := mustMarshal(map[string]any{
		"forward_window_days":      health.ChronicLoadForwardWindowDays,
		"breach_count":             breachCount,
		"breach_threshold":         health.ChronicLoadMinBreachDays,
		"breach_z_threshold":       health.ChronicLoadDeteriorationZThreshold,
		"acute_or_count":           acuteCount,
		"acute_density_threshold":  health.ChronicLoadMinAcuteDensity,
		"per_day":                  cells,
	})

	if err := s.SaveTargetSnapshot(TargetSnapshot{
		Date:              date,
		SubScore:          SubScoreChronicLoad,
		TargetKind:        TargetKindChronicLabel,
		TargetValue:       &chronicLabel,
		Eligible:          true,
		EligibilityReason: health.ChronicLoadEligibilityOK,
		DataCoverage:      cov,
		SourceEpoch:       epoch,
		FormulaVersion:    chronicLoadFormulaVersion,
	}); err != nil {
		return fmt.Errorf("save chronic_label %s: %w", date, err)
	}
	if err := s.SaveTargetSnapshot(TargetSnapshot{
		Date:              date,
		SubScore:          SubScoreChronicLoad,
		TargetKind:        TargetKindChronicAcuteDensity,
		TargetValue:       &acuteDensityLabel,
		Eligible:          true,
		EligibilityReason: health.ChronicLoadEligibilityOK,
		DataCoverage:      cov,
		SourceEpoch:       epoch,
		FormulaVersion:    chronicLoadFormulaVersion,
	}); err != nil {
		return fmt.Errorf("save chronic_acute_density %s: %w", date, err)
	}

	// Update prior-label maps with this date so the naive base-rate
	// baselines for later iterations have access. Per-target_kind to
	// keep distributions honest (mirrors the Acute Risk P2 lesson).
	priorChronic[date] = int(chronicLabel)
	priorAcuteDensity[date] = int(acuteDensityLabel)

	chronicBaseRate := priorEventBaseRate(t, 90, priorChronic)
	acuteDensityBaseRate := priorEventBaseRate(t, 90, priorAcuteDensity)
	for _, b := range []struct {
		tk   string
		rate *float64
	}{
		{TargetKindChronicLabel, chronicBaseRate},
		{TargetKindChronicAcuteDensity, acuteDensityBaseRate},
	} {
		if err := s.SaveNaiveBaseline(NaiveBaseline{
			Date:           date,
			SubScore:       SubScoreChronicLoad,
			TargetKind:     b.tk,
			BaselineKind:   BaselineKindEventBaseRate,
			PredictedValue: b.rate,
			SourceEpoch:    epoch,
			FormulaVersion: chronicLoadFormulaVersion,
		}); err != nil {
			return fmt.Errorf("save chronic base-rate %s/%s: %w", date, b.tk, err)
		}
	}

	return s.SaveFeatureSnapshot(FeatureSnapshot{
		Date:           date,
		SubScore:       SubScoreChronicLoad,
		Features:       featuresJSON,
		SourceEpoch:    epoch,
		FeatureVersion: chronicLoadFeatureVersion,
	})
}

// --- Features ----------------------------------------------------------

type chronicLoadFeatures struct {
	RecoveryToday      *float64 `json:"recovery_3d_today,omitempty"`
	BaselineMean45     *float64 `json:"recovery_3d_baseline_mean_45d,omitempty"`
	BaselineSD45       *float64 `json:"recovery_3d_baseline_sd_45d,omitempty"`
	BaselineZToday     *float64 `json:"recovery_3d_z_today,omitempty"`
	EligibleCount45    int      `json:"recovery_eligible_count_45d"`
	EligibleCount180   int      `json:"recovery_eligible_count_180d"`
	PairedCountToT     int      `json:"paired_count_to_t"`
	WarmupMet          bool     `json:"warmup_met"`
	WarmupComplete45   bool     `json:"warmup_complete_45d"`
	WarmupComplete180  bool     `json:"warmup_complete_180d"`
}

func buildChronicLoadFeatures(
	t time.Time,
	epochStart string,
	recoveryByDate map[string]recoveryRolling3dRow,
	recoveryLookup DailyValueLookup,
	pairedCount int,
	warmupMet bool,
) chronicLoadFeatures {
	out := chronicLoadFeatures{
		PairedCountToT: pairedCount,
		WarmupMet:      warmupMet,
	}
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

// --- Helpers -----------------------------------------------------------

func recoveryRolling3dLookup(rows map[string]recoveryRolling3dRow) DailyValueLookup {
	return func(date string) (*float64, bool) {
		r, ok := rows[date]
		if !ok || !r.Eligible || r.Value == nil {
			return nil, false
		}
		return r.Value, true
	}
}

// countEligibleRecoveryRows counts Recovery rolling_3d rows with
// eligible=true AND non-null value in the trailing 180 days from `t`
// (inclusive), clipped at epochStart. 180d matches the slow baseline
// window and the warmup-evidence horizon used elsewhere.
func countEligibleRecoveryRows(t time.Time, epochStart string, rows map[string]recoveryRolling3dRow) int {
	var n int
	for i := 0; i <= ewmaWindowSlow; i++ {
		ds := t.AddDate(0, 0, -i).Format(isoDate)
		if epochStart != "" && ds < epochStart {
			continue
		}
		r, ok := rows[ds]
		if !ok || !r.Eligible || r.Value == nil {
			continue
		}
		n++
	}
	return n
}

// loadPriorChronicLabels pre-loads chronic_label and
// chronic_acute_density labels for the 90 days before `fromT`. Same
// shape as loadPriorEventLabels in acute_risk_writer.go but for
// Chronic Load's two target_kinds.
func (s *DB) loadPriorChronicLabels(fromT time.Time) (chronicMap, acuteDensityMap map[string]int, err error) {
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
	`, SubScoreChronicLoad, TargetKindChronicLabel, TargetKindChronicAcuteDensity, priorFrom, priorTo)
	if qErr != nil {
		return nil, nil, fmt.Errorf("loadPriorChronicLabels: %w", qErr)
	}
	defer rows.Close()
	chronicMap = make(map[string]int)
	acuteDensityMap = make(map[string]int)
	for rows.Next() {
		var date, kind string
		var v float32
		if scanErr := rows.Scan(&date, &kind, &v); scanErr != nil {
			return nil, nil, fmt.Errorf("loadPriorChronicLabels scan: %w", scanErr)
		}
		val := 0
		if v >= 0.5 {
			val = 1
		}
		switch kind {
		case TargetKindChronicLabel:
			chronicMap[date] = val
		case TargetKindChronicAcuteDensity:
			acuteDensityMap[date] = val
		}
	}
	return chronicMap, acuteDensityMap, rows.Err()
}
