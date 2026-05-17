// Passive Efficiency writer integration test.
//
// Mirrors the Recovery Stability suite (testDB helper, schema-per-test
// isolation, skip-when-no-DB). Test scenarios:
//   - daily_point target for date `t` equals walking_hr(t+1)
//   - rolling_3d equals mean of t+1..t+3 when all three are eligible
//   - one ineligible day (missing OR out-of-range) blocks rolling_3d
//   - feature snapshot for t reflects walking_hr(t), not t+1
//   - naive baselines exist (8 rows: 4 baseline_kinds × 2 target_kinds)
//   - rerun is idempotent

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// seedWalkingHRRow inserts a row into metric_points for the given date
// with metric_name='walking_heart_rate_average' and quality='ok' so
// LoadWalkingHRRows picks it up. Source is fixed; multiple calls for
// the same date are upsert-merged via the metric_points unique key
// (metric_name, date, source).
func seedWalkingHRRow(t *testing.T, db *DB, date string, bpm float64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// metric_points needs a parent health_records row for the FK.
	var hid int64
	if err := db.pool.QueryRow(ctx,
		`INSERT INTO health_records (received_at, payload) VALUES (NOW(), '{}') RETURNING id`,
	).Scan(&hid); err != nil {
		t.Fatalf("seed health_record: %v", err)
	}
	_, err := db.pool.Exec(ctx, `
		INSERT INTO metric_points (health_record_id, metric_name, units, date, qty, source, quality)
		VALUES ($1, 'walking_heart_rate_average', 'count/min', $2, $3, 'integration_test', 'ok')
		ON CONFLICT (metric_name, date, source) DO UPDATE SET qty = excluded.qty, quality = 'ok'
	`, hid, date+" 12:00:00 +0000", bpm)
	if err != nil {
		t.Fatalf("seed walking_hr %s: %v", date, err)
	}
}

func TestPassiveEfficiency_Integration_DailyPointMirrorsNextDay(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Seed 8 contiguous days with the same value. daily_point for t
	// must equal walking_hr(t+1).
	for i := range 8 {
		date := time.Date(2026, 5, 1+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedWalkingHRRow(t, db, date, 101.5)
	}
	if _, err := db.BackfillPassiveEfficiencySnapshots("2026-05-01", "2026-05-04"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	for i := range 4 {
		t.Run(fmt.Sprintf("date_idx_%d", i), func(t *testing.T) {
			date := time.Date(2026, 5, 1+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
			var val float64
			var eligible bool
			var reason string
			err := db.pool.QueryRow(context.Background(), `
				SELECT target_value, eligible, eligibility_reason
				  FROM target_snapshots
				 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
			`, date, SubScorePassiveEfficiency, TargetKindDailyPoint).Scan(&val, &eligible, &reason)
			if err != nil {
				t.Fatalf("read daily_point for %s: %v", date, err)
			}
			if !eligible {
				t.Errorf("daily_point %s: expected eligible, got reason=%q", date, reason)
			}
			if absDiff(val, 101.5) > 1e-4 {
				t.Errorf("daily_point %s value = %v, want ~101.5", date, val)
			}
		})
	}
}

func TestPassiveEfficiency_Integration_Rolling3dMean(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Anchor day, then three distinct values for t+1..t+3.
	seedWalkingHRRow(t, db, "2026-05-01", 100)
	seedWalkingHRRow(t, db, "2026-05-02", 95)
	seedWalkingHRRow(t, db, "2026-05-03", 100)
	seedWalkingHRRow(t, db, "2026-05-04", 105)
	if _, err := db.BackfillPassiveEfficiencySnapshots("2026-05-01", "2026-05-01"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var val float64
	var eligible bool
	var reason string
	err := db.pool.QueryRow(context.Background(), `
		SELECT target_value, eligible, eligibility_reason
		  FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-05-01", SubScorePassiveEfficiency, TargetKindRolling3d).Scan(&val, &eligible, &reason)
	if err != nil {
		t.Fatalf("read rolling_3d: %v", err)
	}
	if !eligible {
		t.Fatalf("rolling_3d expected eligible, got reason=%q", reason)
	}
	if absDiff(val, 100) > 1e-4 {
		t.Errorf("rolling_3d value = %v, want 100", val)
	}
}

func TestPassiveEfficiency_Integration_Rolling3dBlocksOnMissingDay(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// 2026-05-02 missing entirely (no metric_points row).
	seedWalkingHRRow(t, db, "2026-05-01", 100)
	seedWalkingHRRow(t, db, "2026-05-03", 100)
	seedWalkingHRRow(t, db, "2026-05-04", 100)
	if _, err := db.BackfillPassiveEfficiencySnapshots("2026-05-01", "2026-05-01"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var val *float64
	var eligible bool
	var reason string
	var coverage []byte
	err := db.pool.QueryRow(context.Background(), `
		SELECT target_value, eligible, eligibility_reason, data_coverage::text
		  FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-05-01", SubScorePassiveEfficiency, TargetKindRolling3d).Scan(&val, &eligible, &reason, &coverage)
	if err != nil {
		t.Fatalf("read rolling_3d: %v", err)
	}
	if eligible {
		t.Errorf("rolling_3d expected ineligible, got eligible=true val=%v", val)
	}
	if reason != "no_walking_hr" {
		t.Errorf("expected reason no_walking_hr, got %q", reason)
	}
	if !strings.Contains(string(coverage), "2026-05-02") {
		t.Errorf("expected coverage to flag 2026-05-02, got %s", coverage)
	}
}

func TestPassiveEfficiency_Integration_Rolling3dBlocksOnOutOfRange(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// 2026-05-02 has a present-but-implausible value (artifact).
	// Distinct from missing — out-of-range should win priority in
	// firstPassiveBlockingReason.
	seedWalkingHRRow(t, db, "2026-05-01", 100)
	seedWalkingHRRow(t, db, "2026-05-02", 220) // > 180 ceiling
	seedWalkingHRRow(t, db, "2026-05-03", 100)
	seedWalkingHRRow(t, db, "2026-05-04", 100)
	if _, err := db.BackfillPassiveEfficiencySnapshots("2026-05-01", "2026-05-01"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var eligible bool
	var reason string
	if err := db.pool.QueryRow(context.Background(), `
		SELECT eligible, eligibility_reason FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-05-01", SubScorePassiveEfficiency, TargetKindRolling3d).Scan(&eligible, &reason); err != nil {
		t.Fatalf("read rolling_3d: %v", err)
	}
	if eligible {
		t.Error("rolling_3d expected ineligible on out-of-range middle day")
	}
	if reason != "walking_hr_out_of_range" {
		t.Errorf("expected reason walking_hr_out_of_range (priority over missing), got %q", reason)
	}
}

func TestPassiveEfficiency_Integration_FeaturesDoNotLeakFromTplus1(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Day t = 2026-05-10: value 100. Day t+1: value 130 (much higher).
	// features[t].prev_walking_hr must reflect t, not t+1.
	seedWalkingHRRow(t, db, "2026-05-10", 100)
	seedWalkingHRRow(t, db, "2026-05-11", 130)
	seedWalkingHRRow(t, db, "2026-05-12", 100)
	seedWalkingHRRow(t, db, "2026-05-13", 100)
	if _, err := db.BackfillPassiveEfficiencySnapshots("2026-05-10", "2026-05-10"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var features []byte
	if err := db.pool.QueryRow(context.Background(), `
		SELECT features::text FROM feature_snapshots
		 WHERE date = $1 AND sub_score = $2
	`, "2026-05-10", SubScorePassiveEfficiency).Scan(&features); err != nil {
		t.Fatalf("read features: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(features, &parsed); err != nil {
		t.Fatalf("parse features: %v", err)
	}
	prev, ok := parsed["prev_walking_hr"].(float64)
	if !ok {
		t.Fatalf("prev_walking_hr missing or wrong type: %#v", parsed["prev_walking_hr"])
	}
	if absDiff(prev, 100) > 1e-4 {
		t.Errorf("prev_walking_hr = %v, want 100 (= value at t, not t+1)", prev)
	}
}

func TestPassiveEfficiency_Integration_NaiveBaselinesExist(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// 35 contiguous days at 100 bpm.
	for i := range 35 {
		date := time.Date(2026, 4, 1+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedWalkingHRRow(t, db, date, 100)
	}
	if _, err := db.BackfillPassiveEfficiencySnapshots("2026-04-25", "2026-04-25"); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	rows, err := db.pool.Query(context.Background(), `
		SELECT target_kind, baseline_kind, predicted_value
		  FROM naive_baselines
		 WHERE date = $1 AND sub_score = $2
		 ORDER BY target_kind, baseline_kind
	`, "2026-04-25", SubScorePassiveEfficiency)
	if err != nil {
		t.Fatalf("read baselines: %v", err)
	}
	defer rows.Close()
	type row struct {
		tk, bk string
		val    *float64
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.tk, &r.bk, &r.val); err != nil {
			t.Fatalf("scan baseline: %v", err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(got) != 8 {
		t.Fatalf("expected 8 baseline rows, got %d: %+v", len(got), got)
	}
	for _, r := range got {
		if r.val == nil {
			t.Errorf("baseline %s/%s: predicted_value nil", r.tk, r.bk)
			continue
		}
		if absDiff(*r.val, 100) > 1e-4 {
			t.Errorf("baseline %s/%s = %v, want ~100", r.tk, r.bk, *r.val)
		}
	}
}

// TestPassiveEfficiency_Integration_NaiveBaselineReason proves that
// the §6.1 chip's `unknown` reason wiring works end-to-end on a real
// schema:
//
//   1. When a baseline returns NULL because no trailing observations
//      exist within the current epoch, `naive_baselines.reason` is
//      `baseline_warmup`.
//   2. When a baseline returns NULL because the trailing window
//      straddles a source_epoch boundary, `reason` is
//      `baseline_source_epoch_boundary`.
//   3. When a baseline returns a value, `reason` is NULL (joint state
//      invariant from SaveNaiveBaseline).
//
// The chip consumes `ewma_45d` for the deployable continuous layer,
// so the assertions key off that row specifically.
func TestPassiveEfficiency_Integration_NaiveBaselineReason(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Case 1: completely empty walking_hr history → ewma_45d is NULL,
	// no source_epoch override (the bootstrap `initial` epoch starts at
	// 2014-01-01 which is well before any 45d window in 2026), so
	// reason must be `baseline_warmup`.
	if _, err := db.BackfillPassiveEfficiencySnapshots("2026-04-15", "2026-04-15"); err != nil {
		t.Fatalf("warmup backfill: %v", err)
	}
	var reason *string
	var val *float64
	if err := db.pool.QueryRow(context.Background(), `
		SELECT predicted_value, reason
		  FROM naive_baselines
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3 AND baseline_kind = $4
	`, "2026-04-15", SubScorePassiveEfficiency, TargetKindRolling3d, BaselineKindEWMA45d).Scan(&val, &reason); err != nil {
		t.Fatalf("read warmup baseline: %v", err)
	}
	if val != nil {
		t.Errorf("warmup case: predicted_value = %v, want nil", *val)
	}
	if reason == nil || *reason != BaselineReasonWarmup {
		t.Errorf("warmup case: reason = %v, want %q", reason, BaselineReasonWarmup)
	}

	// Case 2: insert a custom source_epoch starting two days before the
	// candidate, so the 45-day EWMA window is clipped to a 3-day slice
	// — even if observations exist, the *window itself* straddles the
	// epoch boundary. Seed 35 observations inside the new epoch so the
	// 45-day window has eligible data but is clipped at the lower end.
	ctx := context.Background()
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO source_epochs(epoch_id, start_date, end_date, kind, description, detected_by, confirmed)
		VALUES ('boundary_test', '2026-05-10', NULL, 'source_epoch', 'epoch-boundary integration test', 'manual', TRUE)
		ON CONFLICT (epoch_id) DO UPDATE SET start_date = EXCLUDED.start_date
	`); err != nil {
		t.Fatalf("seed epoch: %v", err)
	}
	// Close the initial epoch the day before so 2026-05-10..NULL maps
	// to boundary_test.
	if _, err := db.pool.Exec(ctx, `
		UPDATE source_epochs SET end_date = '2026-05-09'
		 WHERE epoch_id = $1
	`, InitialSourceEpoch); err != nil {
		t.Fatalf("close initial epoch: %v", err)
	}
	t.Cleanup(func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer dropCancel()
		_, _ = db.pool.Exec(dropCtx, `UPDATE source_epochs SET end_date = NULL WHERE epoch_id = $1`, InitialSourceEpoch)
		_, _ = db.pool.Exec(dropCtx, `DELETE FROM source_epochs WHERE epoch_id = 'boundary_test'`)
	})

	// Candidate date 2026-05-12 with no observations in the new epoch.
	// 45-day window from 2026-05-12 reaches back to 2026-03-29, but the
	// epoch start clips it to 2026-05-10..2026-05-12 (3 days). With
	// zero observations seeded, value is NULL — but because the window
	// straddled the boundary, reason must be source_epoch_boundary, not
	// warmup.
	if _, err := db.BackfillPassiveEfficiencySnapshots("2026-05-12", "2026-05-12"); err != nil {
		t.Fatalf("boundary backfill: %v", err)
	}
	val = nil
	reason = nil
	if err := db.pool.QueryRow(ctx, `
		SELECT predicted_value, reason
		  FROM naive_baselines
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3 AND baseline_kind = $4
	`, "2026-05-12", SubScorePassiveEfficiency, TargetKindRolling3d, BaselineKindEWMA45d).Scan(&val, &reason); err != nil {
		t.Fatalf("read boundary baseline: %v", err)
	}
	if val != nil {
		t.Errorf("boundary case: predicted_value = %v, want nil", *val)
	}
	if reason == nil || *reason != BaselineReasonSourceEpochBoundary {
		t.Errorf("boundary case: reason = %v, want %q", reason, BaselineReasonSourceEpochBoundary)
	}

	// Case 3: a date with observations available → value populated,
	// reason MUST be NULL (joint-state invariant).
	for i := range 35 {
		date := time.Date(2026, 6, 1+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedWalkingHRRow(t, db, date, 100)
	}
	if _, err := db.BackfillPassiveEfficiencySnapshots("2026-06-25", "2026-06-25"); err != nil {
		t.Fatalf("value-present backfill: %v", err)
	}
	val = nil
	reason = nil
	if err := db.pool.QueryRow(ctx, `
		SELECT predicted_value, reason
		  FROM naive_baselines
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3 AND baseline_kind = $4
	`, "2026-06-25", SubScorePassiveEfficiency, TargetKindRolling3d, BaselineKindEWMA45d).Scan(&val, &reason); err != nil {
		t.Fatalf("read value-present baseline: %v", err)
	}
	if val == nil {
		t.Errorf("value-present case: predicted_value nil, expected non-nil")
	}
	if reason != nil {
		t.Errorf("value-present case: reason = %q, want NULL", *reason)
	}
}

func TestPassiveEfficiency_Integration_IdempotentRerun(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	for i := range 8 {
		date := time.Date(2026, 5, 1+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedWalkingHRRow(t, db, date, 100)
	}
	if _, err := db.BackfillPassiveEfficiencySnapshots("2026-05-01", "2026-05-04"); err != nil {
		t.Fatalf("first: %v", err)
	}
	var n1 int
	if err := db.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM target_snapshots WHERE sub_score = $1`,
		SubScorePassiveEfficiency).Scan(&n1); err != nil {
		t.Fatalf("count1: %v", err)
	}
	var firstAt time.Time
	if err := db.pool.QueryRow(context.Background(), `
		SELECT computed_at FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-05-01", SubScorePassiveEfficiency, TargetKindDailyPoint).Scan(&firstAt); err != nil {
		t.Fatalf("read firstAt: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, err := db.BackfillPassiveEfficiencySnapshots("2026-05-01", "2026-05-04"); err != nil {
		t.Fatalf("second: %v", err)
	}
	var n2 int
	if err := db.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM target_snapshots WHERE sub_score = $1`,
		SubScorePassiveEfficiency).Scan(&n2); err != nil {
		t.Fatalf("count2: %v", err)
	}
	if n1 != n2 {
		t.Errorf("row count drift: first=%d second=%d", n1, n2)
	}
	var secondAt time.Time
	if err := db.pool.QueryRow(context.Background(), `
		SELECT computed_at FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-05-01", SubScorePassiveEfficiency, TargetKindDailyPoint).Scan(&secondAt); err != nil {
		t.Fatalf("read secondAt: %v", err)
	}
	if !secondAt.After(firstAt) {
		t.Errorf("computed_at did not refresh: first=%v second=%v", firstAt, secondAt)
	}
}
