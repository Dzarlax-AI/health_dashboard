// Unit tests for the chip-calibration writer's standalone helpers
// (percentile math + SaveChipCalibration joint-state guards). The
// end-to-end recompute path lives in the integration test below
// because it joins across naive_baselines + target_snapshots on a
// real schema.

package storage

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	cases := []struct {
		name string
		xs   []float64
		p    float64
		want float64
	}{
		{"single value", []float64{0.42}, 80, 0.42},
		{"sorted 1..10 p80 between 8 and 9", []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, 80, 8.2},
		{"unsorted same result", []float64{10, 9, 8, 7, 6, 5, 4, 3, 2, 1}, 80, 8.2},
		{"p0 = min", []float64{1, 2, 3, 4, 5}, 0, 1},
		{"p100 = max", []float64{1, 2, 3, 4, 5}, 100, 5},
		{"p50 of even-length linear", []float64{1, 2, 3, 4}, 50, 2.5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := percentile(tc.xs, tc.p)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Errorf("percentile(%v, %v) = %v, want %v", tc.xs, tc.p, got, tc.want)
			}
		})
	}
}

func TestPercentile_EmptySliceIsNaN(t *testing.T) {
	v := percentile(nil, 80)
	if !math.IsNaN(v) {
		t.Errorf("percentile(nil, 80) = %v, want NaN", v)
	}
}

// SaveChipCalibration enforces the joint-state invariant: any
// non-active status MUST have a NULL cutoff (the row is a
// calibration "this hasn't happened yet" marker, not a deployable
// threshold), and any `active` status MUST have a non-NULL cutoff
// (otherwise the read path would render `calibrating` despite
// status=active).
func TestSaveChipCalibration_JointStateGuard(t *testing.T) {
	v := 0.42
	base := ChipCalibration{
		SubScore:              SubScoreAcuteRisk,
		TargetKind:            TargetKindEventT1T3,
		SourceEpoch:           InitialSourceEpoch,
		Method:                ChipCalibrationMethodPercentileP80,
		CalibrationWindowDays: ChipCalibrationWindowDays,
		NEligible:             100,
		NPositives:            20,
	}

	cases := []struct {
		name    string
		mutate  func(*ChipCalibration)
		wantSub string
	}{
		{
			name: "active without cutoff — rejected",
			mutate: func(c *ChipCalibration) {
				c.Status = ChipCalibrationStatusActive
				c.Cutoff = nil
				c.P80 = &v
				c.BaseRate = &v
			},
			wantSub: "active status requires non-nil cutoff",
		},
		{
			name: "active without p80 — rejected (audit invariant)",
			mutate: func(c *ChipCalibration) {
				c.Status = ChipCalibrationStatusActive
				c.Cutoff = &v
				c.P80 = nil
				c.BaseRate = &v
			},
			wantSub: "active status requires non-nil p80",
		},
		{
			name: "active without base_rate — rejected (audit invariant)",
			mutate: func(c *ChipCalibration) {
				c.Status = ChipCalibrationStatusActive
				c.Cutoff = &v
				c.P80 = &v
				c.BaseRate = nil
			},
			wantSub: "active status requires non-nil base_rate",
		},
		{
			name: "insufficient_eligible with cutoff — rejected",
			mutate: func(c *ChipCalibration) {
				c.Status = ChipCalibrationStatusInsufficientEligible
				c.Cutoff = &v
			},
			wantSub: "cutoff set on non-active",
		},
		{
			name: "insufficient_eligible with p80 — rejected",
			mutate: func(c *ChipCalibration) {
				c.Status = ChipCalibrationStatusInsufficientEligible
				c.P80 = &v
			},
			wantSub: "p80 set on non-active",
		},
		{
			name: "insufficient_positives with base_rate — rejected",
			mutate: func(c *ChipCalibration) {
				c.Status = ChipCalibrationStatusInsufficientPositive
				c.BaseRate = &v
			},
			wantSub: "base_rate set on non-active",
		},
		{
			name: "unknown status string — rejected",
			mutate: func(c *ChipCalibration) {
				c.Status = "something_made_up"
			},
			wantSub: "invalid status",
		},
		{
			name: "unknown method string — rejected",
			mutate: func(c *ChipCalibration) {
				c.Status = ChipCalibrationStatusActive
				c.Cutoff = &v
				c.Method = "made_up_method"
			},
			wantSub: "invalid method",
		},
		{
			name: "empty source_epoch — rejected",
			mutate: func(c *ChipCalibration) {
				c.Status = ChipCalibrationStatusActive
				c.Cutoff = &v
				c.SourceEpoch = ""
			},
			wantSub: "source_epoch",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			db := &DB{}
			err := db.SaveChipCalibration(c)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q should contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}

// Integration: cross-epoch leak guard. Seeds predictions in the
// CURRENT epoch and target labels at the same dates but tagged as
// the OLD epoch — the join in loadChipCalibrationPairs must reject
// the cross-epoch pair, so the writer reports insufficient_eligible
// instead of treating the leak as valid calibration data. Without
// the `AND t.source_epoch = n.source_epoch` join condition this
// test fails: the writer pairs 120 leaked labels with predictions
// and computes a (wrong) cutoff.
func TestRecomputeChipCalibrations_Integration_RejectsCrossEpochLabels(t *testing.T) {
	db, cleanup := testReadinessDB(t)
	defer cleanup()
	ctx := context.Background()

	// Close `initial` on a date in the past, open `new_epoch` starting
	// 120 days ago — so today's date resolves to `new_epoch`.
	today := time.Now().UTC()
	epochStart := today.AddDate(0, 0, -119).Format(isoDate)
	if _, err := db.pool.Exec(ctx,
		"UPDATE source_epochs SET end_date = $1 WHERE epoch_id = $2",
		today.AddDate(0, 0, -120).Format(isoDate), InitialSourceEpoch); err != nil {
		t.Fatalf("close initial: %v", err)
	}
	if err := db.UpsertSourceEpoch(SourceEpoch{
		EpochID: "new_epoch", StartDate: epochStart, Kind: SourceEpochKindIngest,
		Description: "cross-epoch leak guard test", DetectedBy: DetectedByManual, Confirmed: true,
	}); err != nil {
		t.Fatalf("upsert new_epoch: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.pool.Exec(context.Background(),
			`UPDATE source_epochs SET end_date = NULL WHERE epoch_id = $1`, InitialSourceEpoch)
		_, _ = db.pool.Exec(context.Background(),
			`DELETE FROM source_epochs WHERE epoch_id = 'new_epoch'`)
	})

	// Predictions in new_epoch + target labels on the same dates
	// tagged as `initial` — the leak the join must reject.
	baselines := make([]NaiveBaseline, 0, 120)
	labels := make([]targetSnapshotSeed, 0, 120)
	pred := 0.4
	one := 1.0
	for i := 0; i < 120; i++ {
		d := today.AddDate(0, 0, -i).Format(isoDate)
		baselines = append(baselines, NaiveBaseline{
			Date: d, SubScore: SubScoreChronicLoad, TargetKind: TargetKindChronicLabel,
			BaselineKind: BaselineKindEventBaseRate, PredictedValue: &pred,
			SourceEpoch: "new_epoch", FormulaVersion: 1,
		})
		labels = append(labels, targetSnapshotSeed{
			Date: d, SubScore: SubScoreChronicLoad, TargetKind: TargetKindChronicLabel,
			TargetValue: &one, Eligible: true, EligibilityReason: "ok",
			SourceEpoch: InitialSourceEpoch, FormulaVersion: 1,
		})
	}
	seedNaiveBaselinesBulk(t, db, baselines)
	seedTargetSnapshotsBulk(t, db, labels)

	results, err := db.RecomputeChipCalibrations(today.Format(isoDate))
	if err != nil {
		t.Fatalf("RecomputeChipCalibrations: %v", err)
	}
	var chronic *ChipCalibrationRecomputeResult
	for i := range results {
		if results[i].SubScore == SubScoreChronicLoad &&
			results[i].TargetKind == TargetKindChronicLabel {
			chronic = &results[i]
		}
	}
	if chronic == nil || chronic.Saved == nil {
		t.Fatal("missing chronic_label result")
	}
	if chronic.Saved.SourceEpoch != "new_epoch" {
		t.Errorf("calibration written under epoch %q, want new_epoch", chronic.Saved.SourceEpoch)
	}
	// All 120 labels are cross-epoch, so the join must yield zero
	// pairs → insufficient_eligible. A regression on the join
	// condition would instead see all 120 pairs and produce
	// `active` with a populated cutoff.
	if chronic.Saved.Status != ChipCalibrationStatusInsufficientEligible {
		t.Errorf("status = %q, want insufficient_eligible (cross-epoch labels must be rejected by the join)",
			chronic.Saved.Status)
	}
	if chronic.Saved.NEligible != 0 {
		t.Errorf("n_eligible = %d, want 0 (cross-epoch pair leak detected)", chronic.Saved.NEligible)
	}
	if chronic.Saved.Cutoff != nil {
		t.Errorf("cutoff = %v, want NULL on insufficient_eligible", *chronic.Saved.Cutoff)
	}
}

// Integration: end-to-end recompute. Seeds eligible
// (predicted_value, label) pairs over the calibration window and
// asserts the produced row has the expected status + audit fields.
func TestRecomputeChipCalibrations_Integration_PercentileP80(t *testing.T) {
	db, cleanup := testReadinessDB(t)
	defer cleanup()

	// Seed 120 days ending today with monotonically-increasing
	// `predicted_value` for chronic_label so p80 is predictable.
	// Label = 1 on every fourth day → positive rate 25%, well above
	// the 10-positive minimum.
	today := time.Now().UTC()
	baselines := make([]NaiveBaseline, 0, 120)
	labels := make([]targetSnapshotSeed, 0, 120)
	for i := 0; i < 120; i++ {
		d := today.AddDate(0, 0, -i).Format(isoDate)
		pred := 0.1 + 0.005*float64(i) // ranges 0.1 to 0.695
		labelVal := 0.0
		if i%4 == 0 {
			labelVal = 1.0
		}
		baselines = append(baselines, NaiveBaseline{
			Date: d, SubScore: SubScoreChronicLoad,
			TargetKind:     TargetKindChronicLabel,
			BaselineKind:   BaselineKindEventBaseRate,
			PredictedValue: &pred,
			SourceEpoch:    InitialSourceEpoch, FormulaVersion: 1,
		})
		lv := labelVal
		labels = append(labels, targetSnapshotSeed{
			Date: d, SubScore: SubScoreChronicLoad,
			TargetKind:  TargetKindChronicLabel,
			TargetValue: &lv, Eligible: true, EligibilityReason: "ok",
			SourceEpoch: InitialSourceEpoch, FormulaVersion: 1,
		})
	}
	seedNaiveBaselinesBulk(t, db, baselines)
	seedTargetSnapshotsBulk(t, db, labels)

	results, err := db.RecomputeChipCalibrations(today.Format(isoDate))
	if err != nil {
		t.Fatalf("RecomputeChipCalibrations: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (one per chip target), got %d", len(results))
	}

	// Find the chronic_label outcome.
	var chronic *ChipCalibrationRecomputeResult
	for i := range results {
		if results[i].SubScore == SubScoreChronicLoad &&
			results[i].TargetKind == TargetKindChronicLabel {
			chronic = &results[i]
		}
	}
	if chronic == nil {
		t.Fatal("missing chronic_label result")
	}
	if chronic.Error != "" {
		t.Fatalf("chronic_label recompute error: %s", chronic.Error)
	}
	if chronic.Saved == nil {
		t.Fatal("chronic_label saved is nil")
	}
	if chronic.Saved.Status != ChipCalibrationStatusActive {
		t.Errorf("status = %q, want active (120 eligible >= 90; 30 positives >= 10)", chronic.Saved.Status)
	}
	if chronic.Saved.Cutoff == nil {
		t.Fatal("cutoff nil on active status")
	}
	if chronic.Saved.P80 == nil || chronic.Saved.BaseRate == nil {
		t.Fatal("audit fields p80/base_rate not populated")
	}
	// With 120 days seeded but the writer's window is 180 days,
	// only the most recent 120 are eligible (the rest don't exist).
	// p80 of 0.1..0.695 linspace at 80% ≈ 0.1 + 0.005 * 95 ≈ 0.575.
	// Actually since the writer reads ds BETWEEN today-179..today
	// and we seeded today..today-119, all 120 fall in window.
	if chronic.Saved.NEligible != 120 {
		t.Errorf("n_eligible = %d, want 120", chronic.Saved.NEligible)
	}
	if chronic.Saved.NPositives != 30 {
		t.Errorf("n_positives = %d, want 30 (every 4th day of 120)", chronic.Saved.NPositives)
	}
	// Sanity: cutoff = max(p80, base_rate). Base rate = 30/120 = 0.25.
	// p80 across the linspace must exceed 0.25 (p80 ≈ 0.5+), so the
	// max-guard picked p80, not the base_rate floor.
	if *chronic.Saved.P80 < *chronic.Saved.BaseRate {
		t.Errorf("p80 (%v) below base_rate (%v) — guard scenario worth investigating",
			*chronic.Saved.P80, *chronic.Saved.BaseRate)
	}
	if math.Abs(*chronic.Saved.Cutoff-math.Max(*chronic.Saved.P80, *chronic.Saved.BaseRate)) > 1e-9 {
		t.Errorf("cutoff (%v) != max(p80=%v, base_rate=%v)",
			*chronic.Saved.Cutoff, *chronic.Saved.P80, *chronic.Saved.BaseRate)
	}
}

// Integration: insufficient-data paths. Seeds too-small or
// no-positive data so the writer should land on the corresponding
// status enum.
func TestRecomputeChipCalibrations_Integration_InsufficientData(t *testing.T) {
	db, cleanup := testReadinessDB(t)
	defer cleanup()

	today := time.Now().UTC()
	zero := 0.0

	// Acute Risk: only 50 days of paired data (< 90 minimum). All
	// targets are zero so the writer would have nothing to flag
	// even if eligibility passed.
	acuteBaselines := make([]NaiveBaseline, 0, 50)
	acuteLabels := make([]targetSnapshotSeed, 0, 50)
	for i := 0; i < 50; i++ {
		d := today.AddDate(0, 0, -i).Format(isoDate)
		pred := 0.3
		acuteBaselines = append(acuteBaselines, NaiveBaseline{
			Date: d, SubScore: SubScoreAcuteRisk, TargetKind: TargetKindEventT1T3,
			BaselineKind: BaselineKindEventBaseRate, PredictedValue: &pred,
			SourceEpoch: InitialSourceEpoch, FormulaVersion: 1,
		})
		acuteLabels = append(acuteLabels, targetSnapshotSeed{
			Date: d, SubScore: SubScoreAcuteRisk, TargetKind: TargetKindEventT1T3,
			TargetValue: &zero, Eligible: true, EligibilityReason: "ok",
			SourceEpoch: InitialSourceEpoch, FormulaVersion: 1,
		})
	}
	seedNaiveBaselinesBulk(t, db, acuteBaselines)
	seedTargetSnapshotsBulk(t, db, acuteLabels)

	// Chronic Load: 120 eligible days but zero positives.
	chronicBaselines := make([]NaiveBaseline, 0, 120)
	chronicLabels := make([]targetSnapshotSeed, 0, 120)
	for i := 0; i < 120; i++ {
		d := today.AddDate(0, 0, -i).Format(isoDate)
		pred := 0.2
		chronicBaselines = append(chronicBaselines, NaiveBaseline{
			Date: d, SubScore: SubScoreChronicLoad, TargetKind: TargetKindChronicLabel,
			BaselineKind: BaselineKindEventBaseRate, PredictedValue: &pred,
			SourceEpoch: InitialSourceEpoch, FormulaVersion: 1,
		})
		chronicLabels = append(chronicLabels, targetSnapshotSeed{
			Date: d, SubScore: SubScoreChronicLoad, TargetKind: TargetKindChronicLabel,
			TargetValue: &zero, Eligible: true, EligibilityReason: "ok",
			SourceEpoch: InitialSourceEpoch, FormulaVersion: 1,
		})
	}
	seedNaiveBaselinesBulk(t, db, chronicBaselines)
	seedTargetSnapshotsBulk(t, db, chronicLabels)

	results, err := db.RecomputeChipCalibrations(today.Format(isoDate))
	if err != nil {
		t.Fatalf("RecomputeChipCalibrations: %v", err)
	}

	var acute, chronic *ChipCalibrationRecomputeResult
	for i := range results {
		switch results[i].SubScore {
		case SubScoreAcuteRisk:
			acute = &results[i]
		case SubScoreChronicLoad:
			chronic = &results[i]
		}
	}
	if acute == nil || chronic == nil {
		t.Fatalf("missing one of the two targets: %+v", results)
	}
	if acute.Saved == nil || acute.Saved.Status != ChipCalibrationStatusInsufficientEligible {
		t.Errorf("acute status = %v, want insufficient_eligible (50 < 90 minimum)", acute.Saved)
	}
	if acute.Saved != nil && acute.Saved.Cutoff != nil {
		t.Errorf("acute cutoff = %v on insufficient_eligible, want NULL", *acute.Saved.Cutoff)
	}
	if chronic.Saved == nil || chronic.Saved.Status != ChipCalibrationStatusInsufficientPositive {
		t.Errorf("chronic status = %v, want insufficient_positives (0 positives < 10 minimum)", chronic.Saved)
	}
	if chronic.Saved != nil && chronic.Saved.Cutoff != nil {
		t.Errorf("chronic cutoff = %v on insufficient_positives, want NULL", *chronic.Saved.Cutoff)
	}
}
