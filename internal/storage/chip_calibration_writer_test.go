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
		SubScore:    SubScoreAcuteRisk,
		TargetKind:  TargetKindEventT1T3,
		SourceEpoch: InitialSourceEpoch,
		Method:      ChipCalibrationMethodPercentileP80,
		CalibrationWindowDays: ChipCalibrationWindowDays,
		NEligible:   100,
		NPositives:  20,
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

// Integration: end-to-end recompute. Seeds eligible
// (predicted_value, label) pairs over the calibration window and
// asserts the produced row has the expected status + audit fields.
func TestRecomputeChipCalibrations_Integration_PercentileP80(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	// Seed 120 days ending today with monotonically-increasing
	// `predicted_value` for chronic_label so p80 is predictable.
	// Label = 1 on every fourth day → positive rate 25%, well above
	// the 10-positive minimum.
	today := time.Now().UTC()
	for i := 0; i < 120; i++ {
		d := today.AddDate(0, 0, -i).Format(isoDate)
		pred := 0.1 + 0.005*float64(i) // ranges 0.1 to 0.695
		if err := db.SaveNaiveBaseline(NaiveBaseline{
			Date: d,
			SubScore: SubScoreChronicLoad,
			TargetKind: TargetKindChronicLabel,
			BaselineKind: BaselineKindEventBaseRate,
			PredictedValue: &pred,
			SourceEpoch: InitialSourceEpoch,
			FormulaVersion: 1,
		}); err != nil {
			t.Fatalf("seed baseline %s: %v", d, err)
		}
		label := 0.0
		if i%4 == 0 {
			label = 1.0
		}
		if _, err := db.pool.Exec(ctx, `
			INSERT INTO target_snapshots
				(date, sub_score, target_kind, target_value, eligible,
				 eligibility_reason, source_epoch, formula_version, computed_at)
			VALUES ($1, $2, $3, $4, TRUE, 'ok', $5, 1, NOW())
			ON CONFLICT (date, sub_score, target_kind) DO UPDATE SET
				target_value = excluded.target_value,
				eligible = TRUE
		`, d, SubScoreChronicLoad, TargetKindChronicLabel, label, InitialSourceEpoch); err != nil {
			t.Fatalf("seed target %s: %v", d, err)
		}
	}

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
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()

	// Acute Risk: only 50 days of paired data (< 90 minimum).
	today := time.Now().UTC()
	for i := 0; i < 50; i++ {
		d := today.AddDate(0, 0, -i).Format(isoDate)
		pred := 0.3
		if err := db.SaveNaiveBaseline(NaiveBaseline{
			Date: d,
			SubScore: SubScoreAcuteRisk,
			TargetKind: TargetKindEventT1T3,
			BaselineKind: BaselineKindEventBaseRate,
			PredictedValue: &pred,
			SourceEpoch: InitialSourceEpoch,
			FormulaVersion: 1,
		}); err != nil {
			t.Fatalf("seed baseline %s: %v", d, err)
		}
		if _, err := db.pool.Exec(ctx, `
			INSERT INTO target_snapshots
				(date, sub_score, target_kind, target_value, eligible,
				 eligibility_reason, source_epoch, formula_version, computed_at)
			VALUES ($1, $2, $3, $4, TRUE, 'ok', $5, 1, NOW())
		`, d, SubScoreAcuteRisk, TargetKindEventT1T3, 0.0, InitialSourceEpoch); err != nil {
			t.Fatalf("seed target %s: %v", d, err)
		}
	}

	// Chronic Load: 120 eligible days but zero positives.
	for i := 0; i < 120; i++ {
		d := today.AddDate(0, 0, -i).Format(isoDate)
		pred := 0.2
		if err := db.SaveNaiveBaseline(NaiveBaseline{
			Date: d,
			SubScore: SubScoreChronicLoad,
			TargetKind: TargetKindChronicLabel,
			BaselineKind: BaselineKindEventBaseRate,
			PredictedValue: &pred,
			SourceEpoch: InitialSourceEpoch,
			FormulaVersion: 1,
		}); err != nil {
			t.Fatalf("seed chronic baseline %s: %v", d, err)
		}
		if _, err := db.pool.Exec(ctx, `
			INSERT INTO target_snapshots
				(date, sub_score, target_kind, target_value, eligible,
				 eligibility_reason, source_epoch, formula_version, computed_at)
			VALUES ($1, $2, $3, $4, TRUE, 'ok', $5, 1, NOW())
			ON CONFLICT (date, sub_score, target_kind) DO UPDATE SET
				target_value = excluded.target_value
		`, d, SubScoreChronicLoad, TargetKindChronicLabel, 0.0, InitialSourceEpoch); err != nil {
			t.Fatalf("seed chronic target %s: %v", d, err)
		}
	}

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
