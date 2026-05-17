// Acute Risk writer integration test.
//
// Same testDB infrastructure as Recovery / Passive. Validates the five
// methodological invariants the user singled out before the writer
// shipped:
//
//   1. Event label does not leak — per-day baseline excludes the
//      candidate day's own value (windowStatsBefore correctness end-to-end).
//   2. event_strict_t1_t3 does not overwrite event_t1_t3 — distinct PKs,
//      both rows survive a single backfill.
//   3. Warmup gate — fewer than AcuteRiskWarmupMinPaired paired days
//      inside the epoch keeps both target rows ineligible.
//   4. Window catches breach at t+2 — middle of the t+1..t+3 window.
//   5. OR vs AND distinction — HRV-only breach triggers OR but not strict;
//      same-day breach in both channels triggers both.

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/health"
)

// seedAutonomicRow upserts hrv_avg and rhr_avg in daily_scores for a
// single date. Other columns left NULL.
func seedAutonomicRow(t *testing.T, db *DB, date string, hrv, rhr *float64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := db.pool.Exec(ctx, `
		INSERT INTO daily_scores (date, hrv_avg, rhr_avg)
		VALUES ($1, $2, $3)
		ON CONFLICT (date) DO UPDATE SET
			hrv_avg = excluded.hrv_avg,
			rhr_avg = excluded.rhr_avg
	`, date, hrv, rhr)
	if err != nil {
		t.Fatalf("seed autonomic %s: %v", date, err)
	}
}

// seedSteadyHistory writes `days` paired HRV/RHR rows ending at
// `endDate` with small deterministic variation around typical values
// (HRV ~45 with range ±3, RHR ~60 with range ±2). The variation is
// essential: a constant series gives baseline SD=0 which the writer
// (correctly) refuses to z-score, blocking breach detection on
// isolated breach days in tests. The pattern keeps the mean stable so
// breach values like HRV=35 / RHR=75 sit many σ away from baseline.
func seedSteadyHistory(t *testing.T, db *DB, endDate string, days int) {
	t.Helper()
	end, err := time.Parse(isoDate, endDate)
	if err != nil {
		t.Fatalf("parse endDate: %v", err)
	}
	for i := range days {
		d := end.AddDate(0, 0, -i).Format(isoDate)
		hrv := 45.0 + float64((i%7)-3) // 42..48, mean ≈ 45
		rhr := 60.0 + float64((i%5)-2) // 58..62, mean ≈ 60
		seedAutonomicRow(t, db, d, &hrv, &rhr)
	}
}

func TestAcuteRisk_Integration_OREventCaughtAtTPlus2(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// 60 paired warmup days ending the day before our test row (t).
	// Test row t = 2026-04-20. Window = 2026-04-21..04-23. Plant an
	// HRV drop at t+2 only.
	seedSteadyHistory(t, db, "2026-04-19", 60)
	// Window days: t+1, t+2, t+3.
	hrv0, rhr0 := 45.0, 60.0
	seedAutonomicRow(t, db, "2026-04-20", &hrv0, &rhr0) // anchor row
	seedAutonomicRow(t, db, "2026-04-21", &hrv0, &rhr0) // normal
	hrvDrop := 35.0                                     // ~−1.5σ assuming SD~5
	seedAutonomicRow(t, db, "2026-04-22", &hrvDrop, &rhr0)
	seedAutonomicRow(t, db, "2026-04-23", &hrv0, &rhr0) // normal

	if _, err := db.BackfillAcuteRiskSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// Read both target rows for t.
	var orVal, strictVal float32
	var orElig, strictElig bool
	if err := db.pool.QueryRow(context.Background(), `
		SELECT target_value, eligible FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreAcuteRisk, TargetKindEventT1T3).Scan(&orVal, &orElig); err != nil {
		t.Fatalf("read or-event: %v", err)
	}
	if err := db.pool.QueryRow(context.Background(), `
		SELECT target_value, eligible FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreAcuteRisk, TargetKindEventStrictT1T3).Scan(&strictVal, &strictElig); err != nil {
		t.Fatalf("read strict-event: %v", err)
	}

	if !orElig || !strictElig {
		t.Fatalf("expected both eligible (warmup met); got or=%v strict=%v", orElig, strictElig)
	}
	if orVal != 1 {
		t.Errorf("event_t1_t3 expected 1 (HRV breach at t+2), got %v", orVal)
	}
	if strictVal != 0 {
		t.Errorf("event_strict_t1_t3 expected 0 (HRV-only, no RHR spike), got %v", strictVal)
	}
}

func TestAcuteRisk_Integration_StrictRequiresBothSameDay(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	seedSteadyHistory(t, db, "2026-04-19", 60)
	hrv0, rhr0 := 45.0, 60.0
	seedAutonomicRow(t, db, "2026-04-20", &hrv0, &rhr0)
	// t+1: HRV drop only.
	hrvDrop := 35.0
	seedAutonomicRow(t, db, "2026-04-21", &hrvDrop, &rhr0)
	// t+2: RHR spike only.
	rhrSpike := 75.0
	seedAutonomicRow(t, db, "2026-04-22", &hrv0, &rhrSpike)
	// t+3: BOTH breach same day.
	seedAutonomicRow(t, db, "2026-04-23", &hrvDrop, &rhrSpike)

	if _, err := db.BackfillAcuteRiskSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var orVal, strictVal float32
	if err := db.pool.QueryRow(context.Background(), `
		SELECT target_value FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreAcuteRisk, TargetKindEventT1T3).Scan(&orVal); err != nil {
		t.Fatalf("read or: %v", err)
	}
	if err := db.pool.QueryRow(context.Background(), `
		SELECT target_value FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreAcuteRisk, TargetKindEventStrictT1T3).Scan(&strictVal); err != nil {
		t.Fatalf("read strict: %v", err)
	}
	if orVal != 1 {
		t.Errorf("OR event = %v, want 1 (breaches on each of t+1..t+3)", orVal)
	}
	if strictVal != 1 {
		t.Errorf("strict event = %v, want 1 (t+3 has both same-day)", strictVal)
	}
}

func TestAcuteRisk_Integration_StrictDoesNotOverwritePrimary(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	seedSteadyHistory(t, db, "2026-04-19", 60)
	hrv0, rhr0 := 45.0, 60.0
	seedAutonomicRow(t, db, "2026-04-20", &hrv0, &rhr0)
	hrvDrop := 35.0
	seedAutonomicRow(t, db, "2026-04-21", &hrvDrop, &rhr0)
	seedAutonomicRow(t, db, "2026-04-22", &hrv0, &rhr0)
	seedAutonomicRow(t, db, "2026-04-23", &hrv0, &rhr0)

	if _, err := db.BackfillAcuteRiskSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// Both rows must coexist after the run — distinct PKs, primary not
	// overwritten by strict.
	var count int
	if err := db.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2
		   AND target_kind IN ($3, $4)
	`, "2026-04-20", SubScoreAcuteRisk, TargetKindEventT1T3, TargetKindEventStrictT1T3).Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 distinct target rows for acute_risk at 2026-04-20, got %d", count)
	}
}

func TestAcuteRisk_Integration_WarmupGateBlocksLabel(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Only 5 paired warmup days — far below AcuteRiskWarmupMinPaired (30).
	seedSteadyHistory(t, db, "2026-04-19", 5)
	hrv0, rhr0 := 45.0, 60.0
	seedAutonomicRow(t, db, "2026-04-20", &hrv0, &rhr0)
	// Even with extreme values in the window, ineligible state should
	// keep target_value NULL.
	extremeHRV := 10.0
	seedAutonomicRow(t, db, "2026-04-21", &extremeHRV, &rhr0)
	seedAutonomicRow(t, db, "2026-04-22", &extremeHRV, &rhr0)
	seedAutonomicRow(t, db, "2026-04-23", &extremeHRV, &rhr0)

	if _, err := db.BackfillAcuteRiskSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	rows, err := db.pool.Query(context.Background(), `
		SELECT target_kind, eligible, eligibility_reason, target_value
		  FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2
		 ORDER BY target_kind
	`, "2026-04-20", SubScoreAcuteRisk)
	if err != nil {
		t.Fatalf("read targets: %v", err)
	}
	defer rows.Close()
	gotCount := 0
	for rows.Next() {
		var tk, reason string
		var eligible bool
		var val *float32
		if err := rows.Scan(&tk, &eligible, &reason, &val); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if eligible {
			t.Errorf("%s expected ineligible on warmup gate, got eligible=true", tk)
		}
		if reason != health.AcuteRiskEligibilityBaselineWarmup {
			t.Errorf("%s expected reason=baseline_warmup, got %q", tk, reason)
		}
		if val != nil {
			t.Errorf("%s expected NULL target_value when ineligible, got %v", tk, *val)
		}
		gotCount++
	}
	if gotCount != 2 {
		t.Errorf("expected 2 target rows (OR + strict), got %d", gotCount)
	}
}

func TestAcuteRisk_Integration_NoLeakageFromOwnDayIntoLabel(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Active test of the per-candidate baseline rule: t+1 carries an
	// extreme HRV (10) against history that hovers around 45 ± 2.
	//
	//   • Honest behaviour (windowStatsBefore excludes the candidate):
	//     baseline mean ≈ 45, SD ≈ 2, z ≈ −17.5 — far below the −1.5
	//     threshold. OR-event MUST fire: orVal == 1.
	//
	//   • Leaky behaviour (candidate included in own baseline): a
	//     single extreme value drags the mean toward itself and
	//     inflates SD, pushing z close to zero. The breach would
	//     silently disappear: orVal == 0.
	//
	// Asserting orVal == 1 therefore directly fails on label leakage.
	seedSteadyHistory(t, db, "2026-04-19", 60)
	hrv0, rhr0 := 45.0, 60.0
	seedAutonomicRow(t, db, "2026-04-20", &hrv0, &rhr0)
	hrvExtreme := 10.0
	seedAutonomicRow(t, db, "2026-04-21", &hrvExtreme, &rhr0)
	seedAutonomicRow(t, db, "2026-04-22", &hrv0, &rhr0)
	seedAutonomicRow(t, db, "2026-04-23", &hrv0, &rhr0)

	if _, err := db.BackfillAcuteRiskSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var orVal float32
	var cov string
	if err := db.pool.QueryRow(context.Background(), `
		SELECT target_value, data_coverage::text FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreAcuteRisk, TargetKindEventT1T3).Scan(&orVal, &cov); err != nil {
		t.Fatalf("read or: %v", err)
	}
	if orVal != 1 {
		t.Errorf("event_t1_t3 = %v, want 1 — extreme HRV at t+1 must trigger breach against prior-only baseline. If 0, candidate value is leaking into its own baseline.", orVal)
	}
	if !strings.Contains(cov, "2026-04-21") {
		t.Errorf("coverage should reference candidate t+1 (2026-04-21); got: %s", cov)
	}
}

func TestAcuteRisk_Integration_EventBaseRateBaselineExists(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	seedSteadyHistory(t, db, "2026-04-19", 60)
	hrv0, rhr0 := 45.0, 60.0
	for i := range 5 {
		date := fmt.Sprintf("2026-04-%02d", 20+i)
		seedAutonomicRow(t, db, date, &hrv0, &rhr0)
	}

	if _, err := db.BackfillAcuteRiskSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	rows, err := db.pool.Query(context.Background(), `
		SELECT target_kind, baseline_kind, predicted_value
		  FROM naive_baselines
		 WHERE date = $1 AND sub_score = $2
		 ORDER BY target_kind, baseline_kind
	`, "2026-04-20", SubScoreAcuteRisk)
	if err != nil {
		t.Fatalf("read baselines: %v", err)
	}
	defer rows.Close()
	type bRow struct {
		tk, bk string
		val    *float32
	}
	var got []bRow
	for rows.Next() {
		var r bRow
		if err := rows.Scan(&r.tk, &r.bk, &r.val); err != nil {
			t.Fatalf("scan baseline: %v", err)
		}
		got = append(got, r)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 base_rate baseline rows (or + strict), got %d: %+v", len(got), got)
	}
	for _, r := range got {
		if r.bk != BaselineKindEventBaseRate {
			t.Errorf("baseline_kind = %s, want %s", r.bk, BaselineKindEventBaseRate)
		}
	}
}

func TestAcuteRisk_Integration_FeaturesAlwaysWritten(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	seedSteadyHistory(t, db, "2026-04-19", 60)
	hrv0, rhr0 := 45.0, 60.0
	for i := range 5 {
		date := fmt.Sprintf("2026-04-%02d", 20+i)
		seedAutonomicRow(t, db, date, &hrv0, &rhr0)
	}

	if _, err := db.BackfillAcuteRiskSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var features []byte
	if err := db.pool.QueryRow(context.Background(), `
		SELECT features::text FROM feature_snapshots
		 WHERE date = $1 AND sub_score = $2
	`, "2026-04-20", SubScoreAcuteRisk).Scan(&features); err != nil {
		t.Fatalf("read features: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(features, &parsed); err != nil {
		t.Fatalf("parse features: %v", err)
	}
	for _, want := range []string{"hrv_today", "rhr_today", "paired_count_to_t", "warmup_met"} {
		if _, ok := parsed[want]; !ok {
			t.Errorf("features missing %q: %+v", want, parsed)
		}
	}
}

func TestAcuteRisk_Integration_WindowDataMissingBlocksLabel(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Warmup met (60 paired days). Window t+1..t+3 has a fully-missing
	// middle day — t+2 has neither HRV nor RHR. Honest behaviour: mark
	// both target rows ineligible with reason event_window_data_missing,
	// not silently emit event=0. The bleeding edge of any backfill
	// (last 3 dates of a run against today) hits this same gate.
	seedSteadyHistory(t, db, "2026-04-19", 60)
	hrv0, rhr0 := 45.0, 60.0
	seedAutonomicRow(t, db, "2026-04-20", &hrv0, &rhr0)
	seedAutonomicRow(t, db, "2026-04-21", &hrv0, &rhr0) // t+1 has data
	// t+2 (2026-04-22) intentionally NOT seeded — full gap.
	seedAutonomicRow(t, db, "2026-04-23", &hrv0, &rhr0) // t+3 has data

	if _, err := db.BackfillAcuteRiskSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	rows, err := db.pool.Query(context.Background(), `
		SELECT target_kind, eligible, eligibility_reason, target_value, data_coverage::text
		  FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2
		 ORDER BY target_kind
	`, "2026-04-20", SubScoreAcuteRisk)
	if err != nil {
		t.Fatalf("read targets: %v", err)
	}
	defer rows.Close()
	gotCount := 0
	for rows.Next() {
		var tk, reason, cov string
		var eligible bool
		var val *float32
		if err := rows.Scan(&tk, &eligible, &reason, &val, &cov); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if eligible {
			t.Errorf("%s expected ineligible on missing window day, got eligible=true", tk)
		}
		if reason != health.AcuteRiskEligibilityEventWindowDataMissing {
			t.Errorf("%s expected reason=event_window_data_missing, got %q", tk, reason)
		}
		if val != nil {
			t.Errorf("%s expected NULL target_value when ineligible, got %v", tk, *val)
		}
		if !strings.Contains(cov, "2026-04-22") {
			t.Errorf("%s coverage should flag missing 2026-04-22, got: %s", tk, cov)
		}
		gotCount++
	}
	if gotCount != 2 {
		t.Errorf("expected 2 target rows (OR + strict), got %d", gotCount)
	}

	// Baselines must be written even when the forward target window is
	// unobservable — the chip on the bleeding edge of every backfill
	// depends on this. Single-day backfill on a fresh tenant has no
	// prior labels accumulated, so cold-start state is value=NULL with
	// a valid reason. The point of this assertion is row presence and
	// the joint-state invariant; a separate test exercises the
	// value-populated case once prior labels exist.
	baselineRows, err := db.pool.Query(context.Background(), `
		SELECT target_kind, predicted_value, reason
		  FROM naive_baselines
		 WHERE date = $1 AND sub_score = $2 AND baseline_kind = $3
		 ORDER BY target_kind
	`, "2026-04-20", SubScoreAcuteRisk, BaselineKindEventBaseRate)
	if err != nil {
		t.Fatalf("read baselines: %v", err)
	}
	defer baselineRows.Close()
	baselineCount := 0
	for baselineRows.Next() {
		var tk string
		var val *float32
		var reason *string
		if err := baselineRows.Scan(&tk, &val, &reason); err != nil {
			t.Fatalf("scan baseline: %v", err)
		}
		// Joint state: either value present (reason NULL) or value
		// NULL with a valid reason.
		if val != nil && reason != nil {
			t.Errorf("%s: both value=%v and reason=%q set (joint-state)", tk, *val, *reason)
		}
		if val == nil && reason == nil {
			t.Errorf("%s: both value and reason NULL — chip would render unknown without explanation", tk)
		}
		baselineCount++
	}
	if baselineCount != 2 {
		t.Errorf("expected 2 baseline rows (OR + strict) on window-missing date, got %d", baselineCount)
	}
}

// TestAcuteRisk_Integration_BaselinesPopulatedAfterPriorLabels exercises
// the value-populated path: a multi-day backfill where earlier dates
// populate orEventByDate/strictEventByDate, and a later date that hits
// the window-missing gate (because t+1..t+3 has a hole) still gets a
// non-NULL event_base_rate from the accumulated history.
func TestAcuteRisk_Integration_BaselinesPopulatedAfterPriorLabels(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// 60 days of steady history → warmup met. Then 30 days of
	// observable target rows (full t+1..t+3 each) so the writer
	// accumulates a labels-by-date map. The 31st backfill date has a
	// hole in its forward window, triggering event_window_data_missing.
	seedSteadyHistory(t, db, "2026-03-31", 60)
	// Seed observable days April 1..May 3 EXCEPT for May 2 (the hole
	// that triggers window-missing on the backfill date 2026-04-30:
	// t+1=05-01 ok, t+2=05-02 missing, t+3=05-03 ok).
	hrv0, rhr0 := 45.0, 60.0
	skipMay2 := time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC).Format(isoDate)
	for i := 1; i <= 33; i++ {
		date := time.Date(2026, 4, i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		if date == skipMay2 {
			continue
		}
		seedAutonomicRow(t, db, date, &hrv0, &rhr0)
	}

	if _, err := db.BackfillAcuteRiskSnapshots("2026-04-01", "2026-04-30"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// Confirm the last day was target-ineligible (window-missing) and
	// that its event_base_rate baseline still got a populated value
	// from the prior 29 days of accumulated labels.
	var eligible bool
	var reason string
	if err := db.pool.QueryRow(context.Background(), `
		SELECT eligible, eligibility_reason FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-30", SubScoreAcuteRisk, TargetKindEventT1T3).Scan(&eligible, &reason); err != nil {
		t.Fatalf("read target: %v", err)
	}
	if eligible {
		t.Errorf("expected target ineligible on window-missing date, got eligible=true")
	}
	if reason != health.AcuteRiskEligibilityEventWindowDataMissing {
		t.Errorf("target reason = %q, want event_window_data_missing", reason)
	}

	var baseVal *float32
	var baseReason *string
	if err := db.pool.QueryRow(context.Background(), `
		SELECT predicted_value, reason
		  FROM naive_baselines
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3 AND baseline_kind = $4
	`, "2026-04-30", SubScoreAcuteRisk, TargetKindEventT1T3, BaselineKindEventBaseRate).Scan(&baseVal, &baseReason); err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	if baseVal == nil {
		t.Errorf("expected event_base_rate populated from prior 29 days of labels on window-missing date, got NULL (chip would show pending)")
	}
	if baseReason != nil {
		t.Errorf("baseline reason = %q on populated value (joint-state invariant)", *baseReason)
	}
}

// TestAcuteRisk_Integration_BaselineEpochClippingPreventsLeak proves
// that `priorEventBaseRate` resets at source_epoch boundaries. Without
// the clip, the now-unconditional baseline writes (since #110) would
// leak labels from the previous epoch into the first ~90 days of the
// new one — which violates plan §3.4. Operator review on the PR
// flagged this as the load-bearing case after the writer was
// decoupled from forward target eligibility.
//
// Setup:
//   - Old epoch (initial, ends 2026-03-31): 60 days of observable
//     labels. Acute writer accumulates an orEventByDate map for these.
//   - New epoch (`boundary_test`, starts 2026-04-01): the very next
//     date 2026-04-02 has no in-epoch prior labels.
//
// Expectation on 2026-04-02:
//   - `event_base_rate` baseline must be NULL with reason
//     `baseline_source_epoch_boundary` — the 90-day prior window's
//     earliest day (2026-01-02) sits well before the new epoch start.
func TestAcuteRisk_Integration_BaselineEpochClippingPreventsLeak(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	ctx := context.Background()
	// Close the bootstrap initial epoch on 2026-03-31 and open a new
	// one starting 2026-04-01.
	if _, err := db.pool.Exec(ctx,
		"UPDATE source_epochs SET end_date = '2026-03-31' WHERE epoch_id = $1",
		InitialSourceEpoch); err != nil {
		t.Fatalf("close initial epoch: %v", err)
	}
	if err := db.UpsertSourceEpoch(SourceEpoch{
		EpochID: "boundary_test", StartDate: "2026-04-01", Kind: SourceEpochKindIngest,
		Description: "epoch-leak regression", DetectedBy: DetectedByManual, Confirmed: true,
	}); err != nil {
		t.Fatalf("upsert new epoch: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.pool.Exec(context.Background(),
			`UPDATE source_epochs SET end_date = NULL WHERE epoch_id = $1`, InitialSourceEpoch)
		_, _ = db.pool.Exec(context.Background(),
			`DELETE FROM source_epochs WHERE epoch_id = 'boundary_test'`)
	})

	// 60 contiguous observable days in the OLD epoch (Feb–Mar 2026).
	// These produce label rows in orEventByDate when the writer runs.
	seedSteadyHistory(t, db, "2026-01-31", 60)
	hrv0, rhr0 := 45.0, 60.0
	for i := 1; i <= 31; i++ {
		date := time.Date(2026, 2, i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedAutonomicRow(t, db, date, &hrv0, &rhr0)
	}
	for i := 1; i <= 31; i++ {
		date := time.Date(2026, 3, i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedAutonomicRow(t, db, date, &hrv0, &rhr0)
	}
	// Single date in NEW epoch with its t+1..t+3 observable, so we
	// don't trip event_window_data_missing — we want the eligible-target
	// branch's baseline write to be the one under test.
	for i := 2; i <= 6; i++ {
		date := time.Date(2026, 4, i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedAutonomicRow(t, db, date, &hrv0, &rhr0)
	}

	// Backfill across the boundary. Writer pre-loads labels by date
	// (across epochs); the in-helper clip is what must keep the new-
	// epoch baseline NULL.
	if _, err := db.BackfillAcuteRiskSnapshots("2026-04-02", "2026-04-02"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var baseVal *float32
	var baseReason *string
	if err := db.pool.QueryRow(ctx, `
		SELECT predicted_value, reason
		  FROM naive_baselines
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3 AND baseline_kind = $4
	`, "2026-04-02", SubScoreAcuteRisk, TargetKindEventT1T3, BaselineKindEventBaseRate).Scan(&baseVal, &baseReason); err != nil {
		t.Fatalf("read baseline: %v", err)
	}
	if baseVal != nil {
		t.Errorf("baseline leaked across epoch boundary: predicted_value = %v, want NULL", *baseVal)
	}
	if baseReason == nil || *baseReason != BaselineReasonSourceEpochBoundary {
		t.Errorf("baseline reason = %v, want %q", baseReason, BaselineReasonSourceEpochBoundary)
	}
}

func TestAcuteRisk_Integration_BleedingEdgeIneligible(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Backfill ends at the most recent date that has data. The last
	// row in the range has no t+1..t+3 future to observe — Acute Risk
	// must mark it ineligible.
	seedSteadyHistory(t, db, "2026-04-19", 60)
	hrv0, rhr0 := 45.0, 60.0
	// Only seed up to and including 2026-04-20. Nothing for t+1..t+3.
	seedAutonomicRow(t, db, "2026-04-20", &hrv0, &rhr0)

	if _, err := db.BackfillAcuteRiskSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var reason string
	var eligible bool
	if err := db.pool.QueryRow(context.Background(), `
		SELECT eligible, eligibility_reason
		  FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreAcuteRisk, TargetKindEventT1T3).Scan(&eligible, &reason); err != nil {
		t.Fatalf("read: %v", err)
	}
	if eligible {
		t.Error("bleeding-edge row expected ineligible (no future data); got eligible=true")
	}
	if reason != health.AcuteRiskEligibilityEventWindowDataMissing {
		t.Errorf("expected reason=event_window_data_missing, got %q", reason)
	}
}

func TestAcuteRisk_Integration_IdempotentRerun(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	seedSteadyHistory(t, db, "2026-04-19", 60)
	hrv0, rhr0 := 45.0, 60.0
	for i := range 5 {
		date := fmt.Sprintf("2026-04-%02d", 20+i)
		seedAutonomicRow(t, db, date, &hrv0, &rhr0)
	}

	if _, err := db.BackfillAcuteRiskSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("first: %v", err)
	}
	var n1 int
	if err := db.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM target_snapshots WHERE sub_score = $1`,
		SubScoreAcuteRisk).Scan(&n1); err != nil {
		t.Fatalf("count1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := db.BackfillAcuteRiskSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("second: %v", err)
	}
	var n2 int
	if err := db.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM target_snapshots WHERE sub_score = $1`,
		SubScoreAcuteRisk).Scan(&n2); err != nil {
		t.Fatalf("count2: %v", err)
	}
	if n1 != n2 {
		t.Errorf("row count drift on rerun: first=%d second=%d", n1, n2)
	}
}
