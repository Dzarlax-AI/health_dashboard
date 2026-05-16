// Chronic Load writer integration test.
//
// Same testDB infrastructure as the other three writers. Chronic Load
// is upstream-dependent: it reads target_snapshots written by
// Recovery and Acute Risk. Each test must seed those rows directly
// (calling the upstream writers in the test would be slow and
// brittle). The helpers below write minimal target rows of the right
// shape so the Chronic Load writer can do its real per-candidate
// baseline + window-count work.
//
// Invariants under test:
//
//   - Epoch clipping (Recovery rows from a closed prior epoch cannot
//     bias a new-epoch deterioration baseline).
//   - No leakage (each candidate day's baseline excludes its own value).
//   - 5-day deterioration in the 14-day window triggers chronic_label=1.
//   - 4-day deterioration does NOT trigger chronic_label=1.
//   - 3 acute OR events in the window trigger chronic_acute_density=1.
//   - 2 acute events do NOT trigger chronic_acute_density=1.
//   - Warmup gate: fewer than 30 eligible Recovery rolling_3d rows in
//     the source_epoch keeps both target rows ineligible.
//   - Strict-vs-primary acute density: writer counts OR events, not
//     strict — verified by seeding only OR positive rows and getting
//     density = 1.

package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"health-receiver/internal/health"
)

// seedRecoveryRolling3d writes a Recovery rolling_3d target row for a
// date. `value` is the rolling_3d efficiency (0..1); pass nil for an
// explicitly ineligible/missing day.
func seedRecoveryRolling3d(t *testing.T, db *DB, date string, value *float64, epoch string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	eligible := value != nil
	reason := health.SleepEligibilityOK
	if !eligible {
		reason = health.SleepEligibilitySleepTotalOutOfRange
	}
	_, err := db.pool.Exec(ctx, `
		INSERT INTO target_snapshots
			(date, sub_score, target_kind, target_value, eligible,
			 eligibility_reason, source_epoch, formula_version, computed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 1, NOW())
		ON CONFLICT (date, sub_score, target_kind) DO UPDATE SET
			target_value = excluded.target_value,
			eligible = excluded.eligible,
			eligibility_reason = excluded.eligibility_reason,
			source_epoch = excluded.source_epoch,
			computed_at = NOW()
	`,
		date, SubScoreRecoveryStability, TargetKindRolling3d,
		value, eligible, reason, epoch,
	)
	if err != nil {
		t.Fatalf("seed recovery_rolling_3d %s: %v", date, err)
	}
}

// seedAcuteOrEvent writes an Acute Risk event_t1_t3 row. `val` is 0 or 1.
func seedAcuteOrEvent(t *testing.T, db *DB, date string, val float64, epoch string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := db.pool.Exec(ctx, `
		INSERT INTO target_snapshots
			(date, sub_score, target_kind, target_value, eligible,
			 eligibility_reason, source_epoch, formula_version, computed_at)
		VALUES ($1, $2, $3, $4, TRUE, 'ok', $5, 1, NOW())
		ON CONFLICT (date, sub_score, target_kind) DO UPDATE SET
			target_value = excluded.target_value,
			eligible = TRUE,
			eligibility_reason = 'ok',
			source_epoch = excluded.source_epoch,
			computed_at = NOW()
	`, date, SubScoreAcuteRisk, TargetKindEventT1T3, val, epoch)
	if err != nil {
		t.Fatalf("seed acute_or_event %s: %v", date, err)
	}
}

// seedRecoveryHistory writes `days` eligible Recovery rolling_3d rows
// ending at `endDate` with small deterministic variation around
// `baseValue`. Used to satisfy the 30-paired warmup gate and to give
// windowStatsBefore a non-zero SD baseline.
func seedRecoveryHistory(t *testing.T, db *DB, endDate string, days int, baseValue float64, epoch string) {
	t.Helper()
	end, err := time.Parse(isoDate, endDate)
	if err != nil {
		t.Fatalf("parse endDate: %v", err)
	}
	for i := range days {
		d := end.AddDate(0, 0, -i).Format(isoDate)
		// Vary by ±0.02 cycling through 5 phases. With baseValue=0.93
		// the SD comes out around 0.014 — enough to make a
		// rolling_3d of 0.85 sit ~6σ below.
		v := baseValue + float64((i%5)-2)*0.01
		seedRecoveryRolling3d(t, db, d, &v, epoch)
	}
}

func TestChronicLoad_Integration_5DayDeteriorationTriggersLabel(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// 60 days of healthy history, then 5 of 14 forward days with
	// rolling_3d = 0.78 (well below the 0.93 ± 0.014 baseline).
	seedRecoveryHistory(t, db, "2026-04-19", 60, 0.93, InitialSourceEpoch)
	baseHealthy := 0.93
	deteriorated := 0.78
	seedRecoveryRolling3d(t, db, "2026-04-20", &baseHealthy, InitialSourceEpoch)
	// Forward window: t+1..t+14 = 2026-04-21..2026-05-04.
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		// Plant 5 deteriorated days (t+1..t+5), the rest healthy.
		v := baseHealthy
		if i <= 5 {
			v = deteriorated
		}
		seedRecoveryRolling3d(t, db, date, &v, InitialSourceEpoch)
	}

	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var val float32
	var eligible bool
	if err := db.pool.QueryRow(context.Background(), `
		SELECT target_value, eligible FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreChronicLoad, TargetKindChronicLabel).Scan(&val, &eligible); err != nil {
		t.Fatalf("read chronic_label: %v", err)
	}
	if !eligible {
		t.Fatal("expected eligible (warmup met)")
	}
	if val != 1 {
		t.Errorf("chronic_label = %v, want 1 (5 deteriorated days in t+1..t+14)", val)
	}
}

func TestChronicLoad_Integration_4DayDeteriorationDoesNotTrigger(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	seedRecoveryHistory(t, db, "2026-04-19", 60, 0.93, InitialSourceEpoch)
	baseHealthy := 0.93
	deteriorated := 0.78
	seedRecoveryRolling3d(t, db, "2026-04-20", &baseHealthy, InitialSourceEpoch)
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		v := baseHealthy
		if i <= 4 { // 4 deteriorated days — below the 5 threshold
			v = deteriorated
		}
		seedRecoveryRolling3d(t, db, date, &v, InitialSourceEpoch)
	}

	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var val float32
	if err := db.pool.QueryRow(context.Background(), `
		SELECT target_value FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreChronicLoad, TargetKindChronicLabel).Scan(&val); err != nil {
		t.Fatalf("read chronic_label: %v", err)
	}
	if val != 0 {
		t.Errorf("chronic_label = %v, want 0 (only 4 deteriorated days)", val)
	}
}

func TestChronicLoad_Integration_AcuteDensityTriggersOnSevenORs(t *testing.T) {
	// formula_version 2 retune: minimum density threshold is 7 events
	// in 14 days (was 3 in v1). Test that exactly 7 OR events trigger
	// chronic_acute_density = 1.
	db, cleanup := testDB(t)
	defer cleanup()

	seedRecoveryHistory(t, db, "2026-04-19", 60, 0.93, InitialSourceEpoch)
	baseHealthy := 0.93
	seedRecoveryRolling3d(t, db, "2026-04-20", &baseHealthy, InitialSourceEpoch)
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedRecoveryRolling3d(t, db, date, &baseHealthy, InitialSourceEpoch)
	}
	// Seed all 14 forward days so the observability gate is met
	// (`requiredAcuteDays = 14 - 7 + 1 = 8` under v2). Plant 7 OR=1
	// events scattered across the window; the rest as OR=0.
	positiveDays := map[int]bool{2: true, 4: true, 6: true, 8: true, 10: true, 12: true, 14: true}
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		var v float64
		if positiveDays[i] {
			v = 1
		}
		seedAcuteOrEvent(t, db, date, v, InitialSourceEpoch)
	}

	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var val float32
	if err := db.pool.QueryRow(context.Background(), `
		SELECT target_value FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreChronicLoad, TargetKindChronicAcuteDensity).Scan(&val); err != nil {
		t.Fatalf("read chronic_acute_density: %v", err)
	}
	if val != 1 {
		t.Errorf("chronic_acute_density = %v, want 1 (7 OR events in t+1..t+14 hits the v2 threshold)", val)
	}
}

func TestChronicLoad_Integration_AcuteDensityDoesNotTriggerOnSixORs(t *testing.T) {
	// Boundary test: 6 OR events in the 14-day window must NOT trigger
	// the chronic_acute_density label under formula_version 2
	// (threshold = 7).
	db, cleanup := testDB(t)
	defer cleanup()

	seedRecoveryHistory(t, db, "2026-04-19", 60, 0.93, InitialSourceEpoch)
	baseHealthy := 0.93
	seedRecoveryRolling3d(t, db, "2026-04-20", &baseHealthy, InitialSourceEpoch)
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedRecoveryRolling3d(t, db, date, &baseHealthy, InitialSourceEpoch)
	}
	// Seed all 14 forward days so the v2 observability gate
	// (`requiredAcuteDays = 8`) is satisfied. Plant exactly 6 OR=1
	// events — one below the v2 threshold.
	positiveDays := map[int]bool{2: true, 4: true, 6: true, 9: true, 11: true, 13: true}
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		var v float64
		if positiveDays[i] {
			v = 1
		}
		seedAcuteOrEvent(t, db, date, v, InitialSourceEpoch)
	}

	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var val float32
	var eligible bool
	if err := db.pool.QueryRow(context.Background(), `
		SELECT target_value, eligible FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreChronicLoad, TargetKindChronicAcuteDensity).Scan(&val, &eligible); err != nil {
		t.Fatalf("read chronic_acute_density: %v", err)
	}
	if !eligible {
		t.Fatal("expected eligible (14 observed Acute days >= 8 threshold under v2)")
	}
	if val != 0 {
		t.Errorf("chronic_acute_density = %v, want 0 (6 OR events is one below the v2 threshold of 7)", val)
	}
}

func TestChronicLoad_Integration_NearThresholdIncompleteWindowIsIneligible_AcuteDensity(t *testing.T) {
	// Codex review on PR #97 caught this class: when observed positives
	// sit close to the threshold and several window days are missing,
	// the old `requiredAcuteDays = window - threshold + 1` gate let
	// such windows through as `eligible=true, target_value=0`. The
	// fix replaces that with the bidirectional rule — a negative
	// label is honest only when `observed_positives + missing_days <
	// threshold`.
	//
	// Scenario under v2 threshold=7: plant 6 observed OR=1 days, 2
	// observed OR=0 days, and 6 missing days. Max possible positives
	// = 6 (observed) + 6 (missing) = 12, well above the threshold of
	// 7. Any single missing day flipping to 1 would meet the
	// threshold, so the negative label cannot be honestly emitted —
	// the row must be ineligible.
	db, cleanup := testDB(t)
	defer cleanup()

	seedRecoveryHistory(t, db, "2026-04-19", 60, 0.93, InitialSourceEpoch)
	baseHealthy := 0.93
	seedRecoveryRolling3d(t, db, "2026-04-20", &baseHealthy, InitialSourceEpoch)
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedRecoveryRolling3d(t, db, date, &baseHealthy, InitialSourceEpoch)
	}
	// 6 days OR=1, 2 days OR=0, 6 days completely missing (no row).
	positiveDays := map[int]bool{1: true, 2: true, 3: true, 4: true, 5: true, 6: true}
	zeroDays := map[int]bool{7: true, 8: true}
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		if positiveDays[i] {
			seedAcuteOrEvent(t, db, date, 1, InitialSourceEpoch)
		} else if zeroDays[i] {
			seedAcuteOrEvent(t, db, date, 0, InitialSourceEpoch)
		}
		// other days intentionally not seeded
	}

	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var eligible bool
	var reason string
	var val *float32
	if err := db.pool.QueryRow(context.Background(), `
		SELECT eligible, eligibility_reason, target_value FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreChronicLoad, TargetKindChronicAcuteDensity).Scan(&eligible, &reason, &val); err != nil {
		t.Fatalf("read chronic_acute_density: %v", err)
	}
	if eligible {
		t.Errorf("near-threshold incomplete window: expected ineligible (6 observed positives + 6 missing days = max-possible 12 >= threshold 7), got eligible=true target_value=%v", val)
	}
	if reason != EligibilityEventWindowDataMissing {
		t.Errorf("expected reason=event_window_data_missing, got %q", reason)
	}
}

func TestChronicLoad_Integration_NearThresholdIncompleteWindowIsIneligible_ChronicLabel(t *testing.T) {
	// Same bug class for the chronic_label target (threshold = 5
	// breaches in 14d). Plant 4 breaching Recovery days and 5 missing
	// Recovery days. Max possible = 4 + 5 = 9, above threshold 5; any
	// single missing day flipping to a breach hits 5 — negative label
	// cannot be honestly emitted.
	db, cleanup := testDB(t)
	defer cleanup()

	seedRecoveryHistory(t, db, "2026-04-19", 60, 0.93, InitialSourceEpoch)
	baseHealthy := 0.93
	deteriorated := 0.78
	seedRecoveryRolling3d(t, db, "2026-04-20", &baseHealthy, InitialSourceEpoch)
	// 4 deteriorated days, 5 healthy days, 5 days completely missing.
	breachDays := map[int]bool{1: true, 2: true, 3: true, 4: true}
	healthyDays := map[int]bool{5: true, 6: true, 7: true, 8: true, 9: true}
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		switch {
		case breachDays[i]:
			seedRecoveryRolling3d(t, db, date, &deteriorated, InitialSourceEpoch)
		case healthyDays[i]:
			seedRecoveryRolling3d(t, db, date, &baseHealthy, InitialSourceEpoch)
		default:
			// missing: no row at all
		}
	}

	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var eligible bool
	var reason string
	var val *float32
	if err := db.pool.QueryRow(context.Background(), `
		SELECT eligible, eligibility_reason, target_value FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreChronicLoad, TargetKindChronicLabel).Scan(&eligible, &reason, &val); err != nil {
		t.Fatalf("read chronic_label: %v", err)
	}
	if eligible {
		t.Errorf("near-threshold incomplete window: expected ineligible (4 observed breaches + 5 missing days = max-possible 9 >= threshold 5), got eligible=true target_value=%v", val)
	}
	if reason != EligibilityEventWindowDataMissing {
		t.Errorf("expected reason=event_window_data_missing, got %q", reason)
	}
}

func TestChronicLoad_Integration_WindowDataMissingBlocksNegativeLabel(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Bleeding-edge scenario: warmup met but the forward window has
	// NO Recovery or Acute rows downstream (they haven't been
	// backfilled past t). The writer must NOT emit eligible=0 labels
	// — that would silently encode missing-data as "no chronic
	// pattern". Both target rows must be ineligible with
	// event_window_data_missing.
	seedRecoveryHistory(t, db, "2026-04-19", 60, 0.93, InitialSourceEpoch)
	baseHealthy := 0.93
	seedRecoveryRolling3d(t, db, "2026-04-20", &baseHealthy, InitialSourceEpoch)
	// Deliberately seed NOTHING for t+1..t+14.

	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	rows, err := db.pool.Query(context.Background(), `
		SELECT target_kind, eligible, eligibility_reason, target_value
		  FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2
		 ORDER BY target_kind
	`, "2026-04-20", SubScoreChronicLoad)
	if err != nil {
		t.Fatalf("read: %v", err)
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
			t.Errorf("%s expected ineligible on empty forward window, got eligible=true target_value=%v", tk, val)
		}
		if reason != EligibilityEventWindowDataMissing {
			t.Errorf("%s expected reason=event_window_data_missing, got %q", tk, reason)
		}
		if val != nil {
			t.Errorf("%s expected NULL target_value when ineligible, got %v", tk, *val)
		}
		gotCount++
	}
	if gotCount != 2 {
		t.Errorf("expected 2 target rows, got %d", gotCount)
	}
}

func TestChronicLoad_Integration_WarmupGateBlocksLabel(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Only 5 Recovery rows in history — far below the 30 warmup
	// threshold. Even with extreme deteriorations in the window, both
	// target rows must be ineligible.
	seedRecoveryHistory(t, db, "2026-04-19", 5, 0.93, InitialSourceEpoch)
	deteriorated := 0.5
	seedRecoveryRolling3d(t, db, "2026-04-20", &deteriorated, InitialSourceEpoch)
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedRecoveryRolling3d(t, db, date, &deteriorated, InitialSourceEpoch)
	}

	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	rows, err := db.pool.Query(context.Background(), `
		SELECT target_kind, eligible, eligibility_reason, target_value
		  FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2
		 ORDER BY target_kind
	`, "2026-04-20", SubScoreChronicLoad)
	if err != nil {
		t.Fatalf("read: %v", err)
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
		if reason != health.ChronicLoadEligibilityBaselineWarmup {
			t.Errorf("%s expected reason=baseline_warmup, got %q", tk, reason)
		}
		if val != nil {
			t.Errorf("%s expected NULL target_value when ineligible, got %v", tk, *val)
		}
		gotCount++
	}
	if gotCount != 2 {
		t.Errorf("expected 2 target rows, got %d", gotCount)
	}
}

func TestChronicLoad_Integration_EpochClippingPreventsLeakage(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Plant a healthy 60-day history in the closed `initial` epoch
	// (≤2023-12-31) and 30 deteriorated-but-stable values in the
	// `source_2025_current` epoch ending 2026-04-19. The warmup gate
	// must count ONLY the current-epoch rows (30), and the per-day
	// baseline for chronic_label evaluation must exclude pre-epoch
	// rows entirely. Otherwise the deteriorated values in the new
	// epoch would still look normal against the old healthy history.
	//
	// Plant the epochs in source_epochs first so ResolveSourceEpoch
	// can return them.
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `
		UPDATE source_epochs SET end_date = '2023-12-31' WHERE epoch_id = $1
	`, InitialSourceEpoch); err != nil {
		t.Fatalf("close initial epoch: %v", err)
	}
	if err := db.UpsertSourceEpoch(SourceEpoch{
		EpochID: "source_2025_current", StartDate: "2025-01-01", Kind: SourceEpochKindIngest,
		Description: "test epoch", DetectedBy: DetectedByManual, Confirmed: true,
	}); err != nil {
		t.Fatalf("seed 2025 epoch: %v", err)
	}

	// Pre-epoch healthy history (irrelevant to the post-epoch baseline).
	seedRecoveryHistory(t, db, "2023-12-15", 60, 0.95, InitialSourceEpoch)

	// Current-epoch baseline: 30 days of slightly-below-healthy values
	// (mean ~0.85). Their SD is small enough that 0.65 sits well below
	// mean - 1σ for the breach check.
	seedRecoveryHistory(t, db, "2026-04-19", 30, 0.85, "source_2025_current")
	// Anchor day and forward window with 5 deteriorated days inside.
	t0 := 0.85
	seedRecoveryRolling3d(t, db, "2026-04-20", &t0, "source_2025_current")
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		v := 0.85
		if i <= 5 {
			v = 0.65 // ~6σ below the 0.85 ± 0.014 baseline
		}
		seedRecoveryRolling3d(t, db, date, &v, "source_2025_current")
	}

	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var val float32
	var eligible bool
	var sourceEpoch string
	if err := db.pool.QueryRow(context.Background(), `
		SELECT target_value, eligible, source_epoch FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreChronicLoad, TargetKindChronicLabel).Scan(&val, &eligible, &sourceEpoch); err != nil {
		t.Fatalf("read chronic_label: %v", err)
	}
	if sourceEpoch != "source_2025_current" {
		t.Errorf("source_epoch = %q, want source_2025_current", sourceEpoch)
	}
	if !eligible {
		t.Fatal("expected eligible (30 in-epoch rows = warmup met)")
	}
	if val != 1 {
		t.Errorf("chronic_label = %v, want 1 — breach must be detected against current-epoch baseline only", val)
	}
}

func TestChronicLoad_Integration_NoLeakageInPerDayBaseline(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Construct a scenario where naive (own-day-included) baseline
	// would mask a real breach. 60 days of varied history around 0.93,
	// then a 14-day forward window where ALL 14 days have rolling_3d
	// = 0.7 (deteriorated). If the per-day baseline were to include
	// the candidate's own value:
	//   - On t+1, baseline = prior 45 healthy days; z is large neg.
	//   - On t+2, naive baseline would dilute toward t+1's 0.7, etc.
	//   By t+5..t+14 the rolling mean of the baseline would shift
	//   markedly toward 0.7 and the breach count would fall short
	//   of 5. The honest implementation excludes the candidate from
	//   its own baseline → breach count = 14 → label = 1.
	seedRecoveryHistory(t, db, "2026-04-19", 60, 0.93, InitialSourceEpoch)
	baseHealthy := 0.93
	seedRecoveryRolling3d(t, db, "2026-04-20", &baseHealthy, InitialSourceEpoch)
	deteriorated := 0.7
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedRecoveryRolling3d(t, db, date, &deteriorated, InitialSourceEpoch)
	}

	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var val float32
	if err := db.pool.QueryRow(context.Background(), `
		SELECT target_value FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreChronicLoad, TargetKindChronicLabel).Scan(&val); err != nil {
		t.Fatalf("read: %v", err)
	}
	if val != 1 {
		t.Errorf("chronic_label = %v, want 1 — 14-day persistent deterioration must trigger with per-day baselines. If 0, candidate values may be leaking into their own baseline.", val)
	}
}

func TestChronicLoad_Integration_BothTargetsCoexist(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	seedRecoveryHistory(t, db, "2026-04-19", 60, 0.93, InitialSourceEpoch)
	baseHealthy := 0.93
	seedRecoveryRolling3d(t, db, "2026-04-20", &baseHealthy, InitialSourceEpoch)
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedRecoveryRolling3d(t, db, date, &baseHealthy, InitialSourceEpoch)
	}

	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var count int
	if err := db.pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2
		   AND target_kind IN ($3, $4)
	`, "2026-04-20", SubScoreChronicLoad, TargetKindChronicLabel, TargetKindChronicAcuteDensity).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 target rows (chronic_label + chronic_acute_density), got %d", count)
	}
}

func TestChronicLoad_Integration_IdempotentRerun(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	seedRecoveryHistory(t, db, "2026-04-19", 60, 0.93, InitialSourceEpoch)
	baseHealthy := 0.93
	for i := range 5 {
		date := fmt.Sprintf("2026-04-%02d", 20+i)
		seedRecoveryRolling3d(t, db, date, &baseHealthy, InitialSourceEpoch)
	}
	// Need 14 forward days from the latest test date (2026-04-24).
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 24+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedRecoveryRolling3d(t, db, date, &baseHealthy, InitialSourceEpoch)
	}

	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-24"); err != nil {
		t.Fatalf("first: %v", err)
	}
	var n1 int
	if err := db.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM target_snapshots WHERE sub_score = $1`,
		SubScoreChronicLoad).Scan(&n1); err != nil {
		t.Fatalf("count1: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-24"); err != nil {
		t.Fatalf("second: %v", err)
	}
	var n2 int
	if err := db.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM target_snapshots WHERE sub_score = $1`,
		SubScoreChronicLoad).Scan(&n2); err != nil {
		t.Fatalf("count2: %v", err)
	}
	if n1 != n2 {
		t.Errorf("row count drift on rerun: first=%d second=%d", n1, n2)
	}
}

// TestChronicLoad_Integration_PerTenantThresholdOverride proves that
// `settings.chronic_load.min_acute_density` actually flips the writer's
// labelling on a row where the default threshold (7) would not. With
// override = 3 and exactly 4 acute OR events planted in the window:
//
//   - default writer ⇒ acute_count < 7 ⇒ chronic_acute_density = 0
//   - override writer ⇒ acute_count >= 3 ⇒ chronic_acute_density = 1
//
// This is the load-bearing test for per-tenant calibration in §6.2.
// If it ever fails, a second tenant's labels are silently calibrated
// against the `health` distribution.
func TestChronicLoad_Integration_PerTenantThresholdOverride(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Set tenant override BEFORE the writer runs so the loader picks
	// it up. Writer reads cfg once per BackfillChronicLoadSnapshots
	// call.
	if err := db.SaveSettings(map[string]string{
		SettingChronicLoadMinAcuteDensity: "3",
	}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	// Echo back through LoadChronicLoadConfig to make sure the status
	// struct used by the admin endpoint reflects the override before we
	// rely on it.
	cfg, status := db.LoadChronicLoadConfig()
	if cfg.MinAcuteDensity != 3 {
		t.Fatalf("effective MinAcuteDensity = %d, want 3", cfg.MinAcuteDensity)
	}
	if status.MatchesDefaults {
		t.Fatal("status.MatchesDefaults = true after override; expected false")
	}
	if status.CorrectedToDef {
		t.Fatal("status.CorrectedToDef = true on valid override")
	}

	seedRecoveryHistory(t, db, "2026-04-19", 60, 0.93, InitialSourceEpoch)
	baseHealthy := 0.93
	seedRecoveryRolling3d(t, db, "2026-04-20", &baseHealthy, InitialSourceEpoch)
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedRecoveryRolling3d(t, db, date, &baseHealthy, InitialSourceEpoch)
	}
	// Plant 4 OR=1 events. Under the default threshold (7) this would
	// be ineligible (4 < 7 and missing days = 0, so max_possible = 4 < 7
	// — eligible-negative, label = 0). Under override threshold 3,
	// 4 >= 3 ⇒ label = 1, eligible.
	positiveDays := map[int]bool{2: true, 5: true, 9: true, 13: true}
	for i := 1; i <= 14; i++ {
		date := time.Date(2026, 4, 20+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		var v float64
		if positiveDays[i] {
			v = 1
		}
		seedAcuteOrEvent(t, db, date, v, InitialSourceEpoch)
	}

	if _, err := db.BackfillChronicLoadSnapshots("2026-04-20", "2026-04-20"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var val float32
	var eligible bool
	var covJSON []byte
	if err := db.pool.QueryRow(context.Background(), `
		SELECT target_value, eligible, data_coverage::text
		  FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-04-20", SubScoreChronicLoad, TargetKindChronicAcuteDensity).Scan(&val, &eligible, &covJSON); err != nil {
		t.Fatalf("read chronic_acute_density: %v", err)
	}
	if !eligible {
		t.Fatal("override row not eligible; expected eligible-positive at threshold 3")
	}
	if val != 1 {
		t.Errorf("chronic_acute_density = %v, want 1 (override threshold 3, 4 OR events planted)", val)
	}
	// data_coverage must echo the effective threshold the writer used,
	// not the default. This is how analysis scripts know they are
	// looking at override-flipped labels.
	// data_coverage is marshalled via encoding/json which leaves a
	// single space after the colon; check for both shapes to stay
	// resilient to a future compact-encoder swap.
	covStr := string(covJSON)
	if !contains(covStr, "\"acute_density_threshold\":3") && !contains(covStr, "\"acute_density_threshold\": 3") {
		t.Errorf("data_coverage missing override threshold: %s", covStr)
	}
}

// contains is a small substring check kept local so the test file does
// not pull in strings just for this one assertion.
func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
