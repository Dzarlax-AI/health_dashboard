// Recovery Stability writer integration test.
//
// Opt-in via `READINESS_TEST_DSN` or libpq env vars (PGHOST/PGUSER/…).
// Tests skip silently when no connection info is provided so the suite
// stays green on machines without a Postgres available.
//
// Each test run creates its own throwaway schema named
// `rs_test_<unix_nanos>_<pid>` and drops it on cleanup, so prod data
// is never touched even when the test points at the same DSN as the
// running server.
//
// What this covers:
//   - daily_point target for date `t` equals efficiency of the night at
//     daily_scores[t+1] (anchor §3.2)
//   - rolling_3d target equals mean of eff(t+1..t+3) when all three
//     nights are eligible
//   - one ineligible night in the t+1..t+3 window flips rolling_3d to
//     ineligible with no partial average
//   - feature snapshot for `t` does not include eff(t+1)
//   - naive baselines exist for both target_kinds
//   - rerun is idempotent (no duplicate rows, computed_at refreshes)

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// testDB spins up a *DB pointed at a fresh, throwaway schema. Returns
// the DB and a cleanup that drops the schema. Skips the test when no
// connection info is available.
func testDB(t *testing.T) (*DB, func()) {
	t.Helper()

	dsn := os.Getenv("READINESS_TEST_DSN")
	if dsn == "" {
		// Fall back to libpq env vars (PGHOST/PGUSER/…). pgx.ParseConfig
		// with empty string returns config that defers to env.
		if os.Getenv("PGHOST") == "" && os.Getenv("PGDATABASE") == "" {
			t.Skip("READINESS_TEST_DSN unset and no libpq env vars; skipping integration test")
		}
	}

	schema := fmt.Sprintf("rs_test_%d_%d", time.Now().UnixNano(), os.Getpid())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Phase 1: connect on default search_path so we can CREATE SCHEMA
	// without it pre-existing.
	bootstrap, err := New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot connect to test DB: %v", err)
	}
	if err := bootstrap.CreateSchema(ctx, schema); err != nil {
		bootstrap.Close()
		t.Fatalf("create schema %q: %v", schema, err)
	}
	bootstrap.Close()

	// Phase 2: open a pool pinned to the new schema via search_path.
	db, err := NewWithSchema(ctx, dsn, schema)
	if err != nil {
		t.Fatalf("open pool on schema %q: %v", schema, err)
	}
	if err := db.EnsureAllTables(); err != nil {
		db.Close()
		t.Fatalf("EnsureAllTables: %v", err)
	}
	// EnsureIndexes adds metric_points.quality (and partial indexes) via
	// ALTER; Passive Efficiency reads filter `quality='ok'`, so this
	// must run before any writer is exercised.
	db.EnsureIndexes()
	db.EnsureReadinessRedesignTables()
	if err := db.VerifyReadinessRedesignSchema(); err != nil {
		db.Close()
		t.Fatalf("schema not healthy after Ensure: %v", err)
	}

	cleanup := func() {
		dropCtx, dropCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer dropCancel()
		// Reuse the pinned-schema pool for the DROP — search_path doesn't
		// matter for a fully-qualified statement, but we already have a
		// healthy pool.
		_, _ = db.pool.Exec(dropCtx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE")
		db.Close()
	}
	return db, cleanup
}

// seedSleepRow inserts (or upserts) a daily_scores row with the given
// nullable sleep columns. Other columns stay NULL — the writer never
// reads them.
func seedSleepRow(t *testing.T, db *DB, date string, total, deep, rem, core, awake, unspecified *float64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := db.pool.Exec(ctx, `
		INSERT INTO daily_scores
			(date, sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_awake, sleep_unspecified)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (date) DO UPDATE SET
			sleep_total = excluded.sleep_total,
			sleep_deep = excluded.sleep_deep,
			sleep_rem = excluded.sleep_rem,
			sleep_core = excluded.sleep_core,
			sleep_awake = excluded.sleep_awake,
			sleep_unspecified = excluded.sleep_unspecified
	`, date, total, deep, rem, core, awake, unspecified)
	if err != nil {
		t.Fatalf("seed %s: %v", date, err)
	}
}

func TestRecoveryStability_Integration_DailyPointMirrorsNextNight(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Seed 5 contiguous normal nights. Date scheme: anchor on May 1 2026.
	// Each night: total=7.5, awake=0.5 → eff = 7.5 / 8.0 = 0.9375.
	for i := range 8 {
		date := time.Date(2026, 5, 1+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedSleepRow(t, db, date, fp(7.5), fp(1.5), fp(1.8), fp(4.2), fp(0.5), nil)
	}

	from := "2026-05-01"
	to := "2026-05-04" // need t+3 ≤ 2026-05-08, available
	if _, err := db.BackfillRecoveryStabilitySnapshots(from, to); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// daily_point for date t should be eff(t+1) = 0.9375.
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
			`, date, SubScoreRecoveryStability, TargetKindDailyPoint).Scan(&val, &eligible, &reason)
			if err != nil {
				t.Fatalf("read daily_point for %s: %v", date, err)
			}
			if !eligible {
				t.Errorf("daily_point %s: expected eligible, got reason=%q", date, reason)
			}
			if want := 7.5 / 8.0; absDiff(val, want) > 1e-9 {
				t.Errorf("daily_point %s value = %v, want %v", date, val, want)
			}
		})
	}
}

func TestRecoveryStability_Integration_Rolling3dMean(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Three distinct nights so the mean is non-degenerate.
	// 2026-05-02: 7.0 / (7.0 + 1.0) = 0.875
	// 2026-05-03: 7.5 / (7.5 + 0.5) = 0.9375
	// 2026-05-04: 8.0 / (8.0 + 0.4) = 0.9523809…
	// expected rolling_3d for date t=2026-05-01: mean of those three.
	seedSleepRow(t, db, "2026-05-01", fp(7.0), fp(1.5), fp(1.5), fp(4.0), fp(0.4), nil) // anchor (unused as target)
	seedSleepRow(t, db, "2026-05-02", fp(7.0), fp(1.5), fp(1.5), fp(4.0), fp(1.0), nil)
	seedSleepRow(t, db, "2026-05-03", fp(7.5), fp(1.5), fp(1.8), fp(4.2), fp(0.5), nil)
	seedSleepRow(t, db, "2026-05-04", fp(8.0), fp(2.0), fp(2.0), fp(4.0), fp(0.4), nil)

	if _, err := db.BackfillRecoveryStabilitySnapshots("2026-05-01", "2026-05-01"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var val float64
	var eligible bool
	var reason string
	err := db.pool.QueryRow(context.Background(), `
		SELECT target_value, eligible, eligibility_reason
		  FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-05-01", SubScoreRecoveryStability, TargetKindRolling3d).Scan(&val, &eligible, &reason)
	if err != nil {
		t.Fatalf("read rolling_3d: %v", err)
	}
	if !eligible {
		t.Fatalf("rolling_3d expected eligible, got reason=%q", reason)
	}
	want := (7.0/8.0 + 7.5/8.0 + 8.0/8.4) / 3.0
	// target_value column is REAL (float32), ~7 sig digits. Tolerance
	// must absorb the round-trip precision loss; 1e-6 leaves headroom.
	if absDiff(val, want) > 1e-6 {
		t.Errorf("rolling_3d value = %v, want %v", val, want)
	}
}

func TestRecoveryStability_Integration_Rolling3dBlocksOnOneIneligible(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	seedSleepRow(t, db, "2026-05-01", fp(7.5), fp(1.5), fp(1.8), fp(4.2), fp(0.5), nil)
	seedSleepRow(t, db, "2026-05-02", fp(7.5), fp(1.5), fp(1.8), fp(4.2), fp(0.5), nil)
	// t+2 (2026-05-03) is out-of-range: total = 2h, below 4h floor.
	seedSleepRow(t, db, "2026-05-03", fp(2.0), fp(0.5), fp(0.5), fp(1.0), fp(0.3), nil)
	seedSleepRow(t, db, "2026-05-04", fp(7.5), fp(1.5), fp(1.8), fp(4.2), fp(0.5), nil)

	if _, err := db.BackfillRecoveryStabilitySnapshots("2026-05-01", "2026-05-01"); err != nil {
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
	`, "2026-05-01", SubScoreRecoveryStability, TargetKindRolling3d).Scan(&val, &eligible, &reason, &coverage)
	if err != nil {
		t.Fatalf("read rolling_3d: %v", err)
	}
	if eligible {
		t.Errorf("rolling_3d expected ineligible, got eligible=true val=%v", val)
	}
	if val != nil {
		t.Errorf("rolling_3d value expected nil when ineligible, got %v", *val)
	}
	if reason != "sleep_total_out_of_range" {
		t.Errorf("expected reason sleep_total_out_of_range, got %q", reason)
	}
	// data_coverage should mention 2026-05-03 in per_day_reason.
	if !strings.Contains(string(coverage), "2026-05-03") {
		t.Errorf("expected coverage JSON to reference 2026-05-03, got %s", coverage)
	}
}

func TestRecoveryStability_Integration_FeaturesDoNotLeakFromTplus1(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Day t = 2026-05-10: a "normal" night.
	// Day t+1 = 2026-05-11: a wildly different night (eff = 0.5).
	// Feature snapshot for t must reflect eff(t), not eff(t+1).
	seedSleepRow(t, db, "2026-05-10", fp(7.5), fp(1.5), fp(1.8), fp(4.2), fp(0.5), nil) // eff = 0.9375
	seedSleepRow(t, db, "2026-05-11", fp(4.0), fp(0.8), fp(0.8), fp(2.4), fp(4.0), nil) // eff = 0.5
	seedSleepRow(t, db, "2026-05-12", fp(7.5), fp(1.5), fp(1.8), fp(4.2), fp(0.5), nil)
	seedSleepRow(t, db, "2026-05-13", fp(7.5), fp(1.5), fp(1.8), fp(4.2), fp(0.5), nil)

	if _, err := db.BackfillRecoveryStabilitySnapshots("2026-05-10", "2026-05-10"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	var features []byte
	err := db.pool.QueryRow(context.Background(), `
		SELECT features::text FROM feature_snapshots
		 WHERE date = $1 AND sub_score = $2
	`, "2026-05-10", SubScoreRecoveryStability).Scan(&features)
	if err != nil {
		t.Fatalf("read feature snapshot: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(features, &parsed); err != nil {
		t.Fatalf("parse features JSON: %v", err)
	}
	prev, ok := parsed["prev_efficiency"].(float64)
	if !ok {
		t.Fatalf("prev_efficiency missing or wrong type: %#v", parsed["prev_efficiency"])
	}
	// Must equal eff(t = 2026-05-10), NOT eff(t+1).
	wantPrev := 7.5 / 8.0
	if absDiff(prev, wantPrev) > 1e-9 {
		t.Errorf("prev_efficiency = %v, want %v (= eff of day t, not t+1)", prev, wantPrev)
	}
}

func TestRecoveryStability_Integration_NaiveBaselinesExist(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// 30 contiguous normal nights so 7d and 30d means have data.
	for i := range 35 {
		date := time.Date(2026, 4, 1+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedSleepRow(t, db, date, fp(7.5), fp(1.5), fp(1.8), fp(4.2), fp(0.5), nil)
	}

	if _, err := db.BackfillRecoveryStabilitySnapshots("2026-04-25", "2026-04-25"); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	rows, err := db.pool.Query(context.Background(), `
		SELECT target_kind, baseline_kind, predicted_value
		  FROM naive_baselines
		 WHERE date = $1 AND sub_score = $2
		 ORDER BY target_kind, baseline_kind
	`, "2026-04-25", SubScoreRecoveryStability)
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

	// Expect 4 baselines × 2 target_kinds = 8 rows.
	if len(got) != 8 {
		t.Fatalf("expected 8 baseline rows, got %d: %+v", len(got), got)
	}

	// Every baseline should be ≈ 0.9375 since all nights are identical.
	for _, r := range got {
		if r.val == nil {
			t.Errorf("baseline %s/%s: predicted_value nil", r.tk, r.bk)
			continue
		}
		if absDiff(*r.val, 0.9375) > 1e-9 {
			t.Errorf("baseline %s/%s = %v, want ~0.9375", r.tk, r.bk, *r.val)
		}
	}
}

func TestRecoveryStability_Integration_IdempotentRerun(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	for i := range 8 {
		date := time.Date(2026, 5, 1+i, 0, 0, 0, 0, time.UTC).Format(isoDate)
		seedSleepRow(t, db, date, fp(7.5), fp(1.5), fp(1.8), fp(4.2), fp(0.5), nil)
	}

	// First run.
	if _, err := db.BackfillRecoveryStabilitySnapshots("2026-05-01", "2026-05-04"); err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	var n1 int
	if err := db.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM target_snapshots WHERE sub_score = $1`,
		SubScoreRecoveryStability).Scan(&n1); err != nil {
		t.Fatalf("count after first run: %v", err)
	}

	// Capture a computed_at to verify it refreshes (so we can be sure
	// the rerun actually wrote, not no-op'd).
	var firstComputedAt time.Time
	if err := db.pool.QueryRow(context.Background(), `
		SELECT computed_at FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-05-01", SubScoreRecoveryStability, TargetKindDailyPoint).Scan(&firstComputedAt); err != nil {
		t.Fatalf("read first computed_at: %v", err)
	}
	// Sleep a tiny bit so NOW() in upsert is provably later. Postgres
	// NOW() has millisecond resolution; 10ms is more than enough.
	time.Sleep(10 * time.Millisecond)

	// Second run.
	if _, err := db.BackfillRecoveryStabilitySnapshots("2026-05-01", "2026-05-04"); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	var n2 int
	if err := db.pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM target_snapshots WHERE sub_score = $1`,
		SubScoreRecoveryStability).Scan(&n2); err != nil {
		t.Fatalf("count after second run: %v", err)
	}
	if n1 != n2 {
		t.Errorf("rerun produced different row count: first=%d, second=%d (expected idempotent)", n1, n2)
	}

	var secondComputedAt time.Time
	if err := db.pool.QueryRow(context.Background(), `
		SELECT computed_at FROM target_snapshots
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3
	`, "2026-05-01", SubScoreRecoveryStability, TargetKindDailyPoint).Scan(&secondComputedAt); err != nil {
		t.Fatalf("read second computed_at: %v", err)
	}
	if !secondComputedAt.After(firstComputedAt) {
		t.Errorf("computed_at did not refresh on rerun: first=%v, second=%v", firstComputedAt, secondComputedAt)
	}
}

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}
