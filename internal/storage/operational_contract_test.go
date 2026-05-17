// Integration test for LoadOperationalContractRows — proves the
// admin operational-contract preview surface (§6.1) reads the right
// columns and tolerates the three legitimate states the chip can be
// in on any given day:
//
//   1. value present  → predicted_value populated, baseline_reason NULL
//   2. unknown        → predicted_value NULL, baseline_reason set
//   3. pending        → no naive_baselines row at all
//
// We seed each state explicitly rather than relying on writer
// behaviour so the test stays focused on the read-path mapping.

package storage

import (
	"context"
	"testing"
	"time"
)

func TestLoadOperationalContractRows_AllStates(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	ctx := context.Background()

	// Three contiguous dates, one for each chip state on the
	// Recovery / rolling_3d / ewma_45d row (chip[0] in chipConfigs).
	const valueDate = "2026-05-10"
	const unknownDate = "2026-05-11"
	const pendingDate = "2026-05-12"

	// --- value present: predicted_value set, no reason
	v := 0.91
	if err := db.SaveNaiveBaseline(NaiveBaseline{
		Date:           valueDate,
		SubScore:       SubScoreRecoveryStability,
		TargetKind:     TargetKindRolling3d,
		BaselineKind:   BaselineKindEWMA45d,
		PredictedValue: &v,
		SourceEpoch:    InitialSourceEpoch,
		FormulaVersion: 1,
	}); err != nil {
		t.Fatalf("seed value-present row: %v", err)
	}
	// Sibling target_snapshots — eligible row so the secondary
	// diagnostic surfaces.
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO target_snapshots
			(date, sub_score, target_kind, target_value, eligible,
			 eligibility_reason, source_epoch, formula_version, computed_at)
		VALUES ($1, $2, $3, $4, TRUE, 'ok', $5, 1, NOW())
	`, valueDate, SubScoreRecoveryStability, TargetKindRolling3d, 0.92, InitialSourceEpoch); err != nil {
		t.Fatalf("seed target_snapshot for value date: %v", err)
	}

	// --- unknown: predicted_value NULL, reason set
	if err := db.SaveNaiveBaseline(NaiveBaseline{
		Date:           unknownDate,
		SubScore:       SubScoreRecoveryStability,
		TargetKind:     TargetKindRolling3d,
		BaselineKind:   BaselineKindEWMA45d,
		PredictedValue: nil,
		Reason:         BaselineReasonWarmup,
		SourceEpoch:    InitialSourceEpoch,
		FormulaVersion: 1,
	}); err != nil {
		t.Fatalf("seed unknown row: %v", err)
	}
	// Sibling target_snapshots with a different eligibility reason —
	// proves we report baseline-side and target-side reasons
	// independently (per §6.1, the two writers can disagree).
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO target_snapshots
			(date, sub_score, target_kind, target_value, eligible,
			 eligibility_reason, source_epoch, formula_version, computed_at)
		VALUES ($1, $2, $3, NULL, FALSE, 'sleep_data_missing', $4, 1, NOW())
	`, unknownDate, SubScoreRecoveryStability, TargetKindRolling3d, InitialSourceEpoch); err != nil {
		t.Fatalf("seed target_snapshot for unknown date: %v", err)
	}

	// --- pending: only a target row (e.g. backfill ran target writer
	// but baseline writer hasn't caught up yet). LoadOperationalContractRows
	// must surface this as `NULL predicted_value, NULL baseline_reason`
	// so the handler renders `pending`.
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO target_snapshots
			(date, sub_score, target_kind, target_value, eligible,
			 eligibility_reason, source_epoch, formula_version, computed_at)
		VALUES ($1, $2, $3, 0.93, TRUE, 'ok', $4, 1, NOW())
	`, pendingDate, SubScoreRecoveryStability, TargetKindRolling3d, InitialSourceEpoch); err != nil {
		t.Fatalf("seed target_snapshot for pending date: %v", err)
	}

	rows, err := db.LoadOperationalContractRows("2026-05-10", "2026-05-12")
	if err != nil {
		t.Fatalf("LoadOperationalContractRows: %v", err)
	}

	// Find the Recovery rows by date so we can assert each state
	// independently of the chip-order sort within a date.
	byDate := map[string]OperationalContractRow{}
	for _, r := range rows {
		if r.SubScore == SubScoreRecoveryStability {
			byDate[r.Date] = r
		}
	}

	got, ok := byDate[valueDate]
	if !ok {
		t.Fatalf("missing recovery row for value date")
	}
	if got.PredictedValue == nil || *got.PredictedValue < 0.90 || *got.PredictedValue > 0.92 {
		t.Errorf("value date: predicted_value = %v, want ~0.91", got.PredictedValue)
	}
	if got.BaselineReason != nil {
		t.Errorf("value date: baseline_reason = %q, want NULL", *got.BaselineReason)
	}
	if got.TargetEligibilityReason == nil || *got.TargetEligibilityReason != "ok" {
		t.Errorf("value date: target_eligibility_reason = %v, want \"ok\"", got.TargetEligibilityReason)
	}

	got, ok = byDate[unknownDate]
	if !ok {
		t.Fatalf("missing recovery row for unknown date")
	}
	if got.PredictedValue != nil {
		t.Errorf("unknown date: predicted_value = %v, want NULL", *got.PredictedValue)
	}
	if got.BaselineReason == nil || *got.BaselineReason != BaselineReasonWarmup {
		t.Errorf("unknown date: baseline_reason = %v, want %q", got.BaselineReason, BaselineReasonWarmup)
	}
	if got.TargetEligibilityReason == nil || *got.TargetEligibilityReason != "sleep_data_missing" {
		t.Errorf("unknown date: target_eligibility_reason = %v, want \"sleep_data_missing\"", got.TargetEligibilityReason)
	}

	got, ok = byDate[pendingDate]
	if !ok {
		t.Fatalf("missing recovery row for pending date — pending state must still appear in the result set so the operator sees the gap")
	}
	if got.PredictedValue != nil {
		t.Errorf("pending date: predicted_value = %v, want NULL", *got.PredictedValue)
	}
	if got.BaselineReason != nil {
		t.Errorf("pending date: baseline_reason = %q, want NULL", *got.BaselineReason)
	}
	if got.TargetEligibilityReason == nil || *got.TargetEligibilityReason != "ok" {
		t.Errorf("pending date: target_eligibility_reason = %v, want \"ok\" (target row exists, baseline doesn't)", got.TargetEligibilityReason)
	}
}

func TestLoadOperationalContractRows_OrderingAndScope(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Seed one baseline per chip config on a single date so we can
	// assert chip-order sort.
	const date = "2026-05-10"
	v := 0.5
	for _, c := range chipConfigs {
		if err := db.SaveNaiveBaseline(NaiveBaseline{
			Date:           date,
			SubScore:       c.SubScore,
			TargetKind:     c.TargetKind,
			BaselineKind:   c.BaselineKind,
			PredictedValue: &v,
			SourceEpoch:    InitialSourceEpoch,
			FormulaVersion: 1,
		}); err != nil {
			t.Fatalf("seed %s: %v", c.SubScore, err)
		}
	}

	// Off-config row that must NOT appear in the result set — proves
	// the query filters to the four deployable chip configurations
	// only, not every baseline written.
	if err := db.SaveNaiveBaseline(NaiveBaseline{
		Date:           date,
		SubScore:       SubScoreRecoveryStability,
		TargetKind:     TargetKindRolling3d,
		BaselineKind:   BaselineKindRolling7dMean, // not the chip's deployable baseline
		PredictedValue: &v,
		SourceEpoch:    InitialSourceEpoch,
		FormulaVersion: 1,
	}); err != nil {
		t.Fatalf("seed off-config: %v", err)
	}

	rows, err := db.LoadOperationalContractRows(date, date)
	if err != nil {
		t.Fatalf("LoadOperationalContractRows: %v", err)
	}
	if len(rows) != len(chipConfigs) {
		t.Fatalf("expected %d rows (one per chip), got %d: %+v", len(chipConfigs), len(rows), rows)
	}
	for i, c := range chipConfigs {
		got := rows[i]
		if got.SubScore != c.SubScore {
			t.Errorf("row[%d].sub_score = %q, want %q", i, got.SubScore, c.SubScore)
		}
		if got.BaselineKind != c.BaselineKind {
			t.Errorf("row[%d].baseline_kind = %q, want %q (off-config rows must be filtered out)",
				i, got.BaselineKind, c.BaselineKind)
		}
	}
	// Sanity: rolling_7d_mean must not appear regardless of order.
	for _, r := range rows {
		if r.BaselineKind == BaselineKindRolling7dMean {
			t.Errorf("rolling_7d_mean leaked into result set")
		}
	}

	// Date ordering: descending. Seed one more row on an earlier date.
	earlier := "2026-05-08"
	if err := db.SaveNaiveBaseline(NaiveBaseline{
		Date:           earlier,
		SubScore:       chipConfigs[0].SubScore,
		TargetKind:     chipConfigs[0].TargetKind,
		BaselineKind:   chipConfigs[0].BaselineKind,
		PredictedValue: &v,
		SourceEpoch:    InitialSourceEpoch,
		FormulaVersion: 1,
	}); err != nil {
		t.Fatalf("seed earlier date: %v", err)
	}
	rows, err = db.LoadOperationalContractRows(earlier, date)
	if err != nil {
		t.Fatalf("LoadOperationalContractRows (multi-date): %v", err)
	}
	if len(rows) == 0 || rows[0].Date != date {
		t.Errorf("first row date = %q, want %q (descending order)", rows[0].Date, date)
	}
	if rows[len(rows)-1].Date != earlier {
		t.Errorf("last row date = %q, want %q (descending order)", rows[len(rows)-1].Date, earlier)
	}

	_ = time.Now // keep time import in case the helper grows date-relative logic later
}
