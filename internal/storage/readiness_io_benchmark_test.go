package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestReadinessIOPerfHarness is an opt-in reproducible before/after harness.
// It uses only the isolated integration-test database selected by testdb.DSN.
// Set READINESS_IO_BENCH=1 and HEALTH_DB_TESTS=1 to run it.
func TestReadinessIOPerfHarness(t *testing.T) {
	if testing.Short() || os.Getenv("READINESS_IO_BENCH") != "1" {
		t.Skip("set READINESS_IO_BENCH=1 to run the isolated PostgreSQL performance harness")
	}
	db, cleanup := testDB(t)
	defer cleanup()

	today := time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC)
	wantParity := map[int]struct {
		rows, serving string
	}{
		1:  {rows: "951fa18d8b08e20ff1288e74356b85ff", serving: "1da42cb1afec09da60c3cb0b5616b91f48794d5042bbce384bc3482fe740aa6f"},
		7:  {rows: "8fd4ab8cfd4572d7ff61b20e24e5ddae", serving: "8320a1af250c0094f8b797fe30f47a903885d28a489bc340119b3d0b8f83b369"},
		30: {rows: "110fce03742084a709b06af9837ba22c", serving: "d4f34e4b7ba1745541c92ec0866e49b305fcd01e690e5b2186e81d3fd4a77a47"},
	}
	for _, affectedDays := range []int{1, 7, 30} {
		resetFullTestDB(t, db)
		seedReadinessIOFixture(t, db, today)
		affectedFrom := today.AddDate(0, 0, -affectedDays+1).Format(isoDate)
		to := today.Format(isoDate)
		from, expandedTo, ok := readinessRedesignRoutineWindow([]string{affectedFrom, to}, today)
		if !ok || expandedTo != to {
			t.Fatalf("routine window for %d affected days = %s..%s, ok=%v", affectedDays, from, expandedTo, ok)
		}
		writers := []struct {
			name string
			run  func(from, to string) (int, error)
		}{
			{name: "recovery_stability", run: db.BackfillRecoveryStabilitySnapshots},
			{name: "passive_efficiency", run: db.BackfillPassiveEfficiencySnapshots},
			{name: "acute_risk", run: db.BackfillAcuteRiskSnapshots},
			{name: "chronic_load", run: db.BackfillChronicLoadSnapshots},
		}
		var coldDigest string
		for _, pass := range []string{"cold", "warm"} {
			for _, writer := range writers {
				resetReadinessIOStats(t, db)
				started := time.Now()
				written, err := writer.run(from, to)
				elapsed := time.Since(started)
				if err != nil {
					t.Fatalf("%s %s %d-day scenario: %v", pass, writer.name, affectedDays, err)
				}
				stats, err := readinessIOQueryStats(db)
				if err != nil {
					t.Fatalf("query count: %v", err)
				}
				t.Logf("readiness_io pass=%s affected=%d expanded=%s..%s writer=%s rows=%d elapsed=%s queries=%d epoch_queries=%d epoch_ms=%.3f target_queries=%d feature_queries=%d baseline_queries=%d", pass, affectedDays, from, to, writer.name, written, elapsed, stats.totalCalls, stats.epochCalls, stats.epochMS, stats.targetCalls, stats.featureCalls, stats.baselineCalls)
			}
			digest := readinessIOOutputDigest(t, db)
			if pass == "cold" {
				coldDigest = digest
			} else if digest != coldDigest {
				t.Fatalf("cold/warm output mismatch for %d-day scenario: %s != %s", affectedDays, coldDigest, digest)
			}
		}
		for _, pass := range []string{"orchestrator_cold", "orchestrator_warm"} {
			if pass == "orchestrator_cold" {
				clearReadinessIOOutputs(t, db)
			}
			resetReadinessIOStats(t, db)
			started := time.Now()
			if err := db.runReadinessRedesignBackfillRange(from, to); err != nil {
				t.Fatalf("%s orchestrated %d-day scenario: %v", pass, affectedDays, err)
			}
			elapsed := time.Since(started)
			stats, err := readinessIOQueryStats(db)
			if err != nil {
				t.Fatalf("orchestrator query count: %v", err)
			}
			rowsDigest := readinessIOOutputDigest(t, db)
			if rowsDigest != wantParity[affectedDays].rows {
				t.Fatalf("orchestrator output mismatch for %d affected days: %s", affectedDays, rowsDigest)
			}
			t.Logf("readiness_io pass=%s affected=%d expanded=%s..%s elapsed=%s queries=%d epoch_queries=%d epoch_ms=%.3f target_queries=%d feature_queries=%d baseline_queries=%d", pass, affectedDays, from, to, elapsed, stats.totalCalls, stats.epochCalls, stats.epochMS, stats.targetCalls, stats.featureCalls, stats.baselineCalls)
		}
		rowsDigest := readinessIOOutputDigest(t, db)
		servingDigest := readinessIOServingDigest(t, db, to)
		want := wantParity[affectedDays]
		if rowsDigest != want.rows || servingDigest != want.serving {
			t.Fatalf("old/new parity mismatch for %d affected days: rows=%s serving=%s; want rows=%s serving=%s", affectedDays, rowsDigest, servingDigest, want.rows, want.serving)
		}
		t.Logf("readiness_io affected=%d parity_digest=%s serving_digest=%s", affectedDays, rowsDigest, servingDigest)
	}
}

func clearReadinessIOOutputs(t *testing.T, db *DB) {
	t.Helper()
	if _, err := db.pool.Exec(context.Background(), `TRUNCATE target_snapshots, feature_snapshots, naive_baselines`); err != nil {
		t.Fatalf("clear readiness outputs: %v", err)
	}
}

func readinessIOServingDigest(t *testing.T, db *DB, asOf string) string {
	t.Helper()
	summary, err := db.LoadReadinessMonitoringSummary(asOf)
	if err != nil {
		t.Fatalf("load readiness monitoring serving output: %v", err)
	}
	serialized, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal readiness monitoring serving output: %v", err)
	}
	hash := sha256.Sum256(serialized)
	return hex.EncodeToString(hash[:])
}

func seedReadinessIOFixture(t *testing.T, db *DB, today time.Time) {
	t.Helper()
	ctx := context.Background()
	start := today.AddDate(0, 0, -365).Format(isoDate)
	end := today.AddDate(0, 0, 3).Format(isoDate)
	_, err := db.pool.Exec(ctx, `
		INSERT INTO daily_scores
			(date, hrv_avg, rhr_avg, sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_awake)
		SELECT d::date::text, 55 + (extract(doy from d)::int % 7), 55 + (extract(doy from d)::int % 3),
		       7.2, 1.4, 1.8, 4.0, 0.4
		  FROM generate_series($1::date, $2::date, interval '1 day') AS d
	`, start, end)
	if err != nil {
		t.Fatalf("seed daily scores: %v", err)
	}
	_, err = db.pool.Exec(ctx, `
		WITH inserted AS (
			INSERT INTO health_records (received_at, payload)
			SELECT NOW(), '{}' FROM generate_series($1::date, $2::date, interval '1 day')
			RETURNING id
		), dates AS (
			SELECT id, ($1::date + (row_number() OVER (ORDER BY id) - 1)::int)::date AS d FROM inserted
		)
		INSERT INTO metric_points (health_record_id, metric_name, units, date, qty, source, quality)
		SELECT id, 'walking_heart_rate_average', 'count/min', d::text || ' 12:00:00 +0000',
		       95 + (extract(doy from d)::int % 5), 'readiness_io_fixture', 'ok'
		  FROM dates
	`, start, end)
	if err != nil {
		t.Fatalf("seed walking heart rate: %v", err)
	}
}

func resetReadinessIOStats(t *testing.T, db *DB) {
	t.Helper()
	if _, err := db.pool.Exec(context.Background(), `SELECT public.pg_stat_statements_reset()`); err != nil {
		t.Fatalf("reset pg_stat_statements: %v", err)
	}
}

type readinessIOStats struct {
	totalCalls, epochCalls, targetCalls, featureCalls, baselineCalls int64
	epochMS                                                          float64
}

func readinessIOQueryStats(db *DB) (readinessIOStats, error) {
	var stats readinessIOStats
	err := db.pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(calls), 0)::bigint,
		       COALESCE(SUM(calls) FILTER (WHERE query ILIKE '%source_epochs%'), 0)::bigint,
		       COALESCE(SUM(calls) FILTER (WHERE query ILIKE '%INSERT INTO target_snapshots%'), 0)::bigint,
		       COALESCE(SUM(calls) FILTER (WHERE query ILIKE '%INSERT INTO feature_snapshots%'), 0)::bigint,
		       COALESCE(SUM(calls) FILTER (WHERE query ILIKE '%INSERT INTO naive_baselines%'), 0)::bigint,
	       COALESCE(SUM(total_exec_time) FILTER (WHERE query ILIKE '%source_epochs%'), 0)::double precision
		  FROM public.pg_stat_statements
		 WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
		   AND query NOT ILIKE '%pg_stat_statements%'
	`).Scan(&stats.totalCalls, &stats.epochCalls, &stats.targetCalls, &stats.featureCalls, &stats.baselineCalls, &stats.epochMS)
	return stats, err
}

func readinessIOOutputDigest(t *testing.T, db *DB) string {
	t.Helper()
	// Deliberately omit computed_at: it records run time and is not an
	// algorithmic output. All serving fields, inputs, and formula versions stay
	// in the digest.
	var digest string
	err := db.pool.QueryRow(context.Background(), `
		SELECT md5(
			COALESCE((SELECT string_agg(row_to_json(x)::text, E'\n' ORDER BY date, sub_score, target_kind)
			            FROM (SELECT date, sub_score, target_kind, target_value, eligible, eligibility_reason,
			                         data_coverage::text, source_epoch, formula_version FROM target_snapshots) x), '') || '|' ||
			COALESCE((SELECT string_agg(row_to_json(x)::text, E'\n' ORDER BY date, sub_score)
			            FROM (SELECT date, sub_score, features::text, source_epoch, feature_version FROM feature_snapshots) x), '') || '|' ||
			COALESCE((SELECT string_agg(row_to_json(x)::text, E'\n' ORDER BY date, sub_score, target_kind, baseline_kind)
			            FROM (SELECT date, sub_score, target_kind, baseline_kind, predicted_value, reason,
			                         source_epoch, formula_version FROM naive_baselines) x), '')
		)
	`).Scan(&digest)
	if err != nil {
		t.Fatalf("read parity digest: %v", err)
	}
	return digest
}
