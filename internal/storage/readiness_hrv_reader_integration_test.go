package storage

import (
	"context"
	"encoding/json"
	"os"

	"github.com/jackc/pgx/v5"
	"slices"
	"testing"

	"health-receiver/internal/health"
)

// The two production readers use different prior days. A correction may also
// arrive after the morning decision. Keep a copy of the actual morning input
// before mutating the synthetic database, so the test cannot backdate it.
func TestReadinessHRVReaderInputs(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	id := insertTestRawRecord(t, db, "readiness-hrv-reader")
	const anchor = "2026-03-31"
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO metric_points (health_record_id,metric_name,units,date,qty,source,quality)
		SELECT $1,'heart_rate_variability','ms',('2026-03-31'::date-d)::text || ' 07:00:00 +0000',50+d,'watch','ok'
		FROM generate_series(0,10) d WHERE d <> 4`, id)
	exec(`INSERT INTO metric_points (health_record_id,metric_name,units,date,qty,source,quality) VALUES
		($1,'heart_rate_variability','ms','2026-03-31 08:00:00 +0000',70,'ring','ok'),
		($1,'heart_rate_variability','ms','2026-03-31 09:00:00 +0000',999,'watch','invalid'),
		($1,'resting_heart_rate','bpm','2026-03-31 07:00:00 +0000',55,'watch','ok')`, id)
	exec(`INSERT INTO daily_scores (date,hrv_avg)
		SELECT ('2026-03-31'::date-d)::text,40+d FROM generate_series(0,10) d WHERE d <> 4`)
	readMorning := func() *health.RawMetrics {
		t.Helper()
		data := db.rawMetricsFromDailyScoresAt(anchor, true)
		if data == nil || data.ReadinessEvidence == nil {
			t.Fatal("expected a populated cache-backed morning window")
		}
		return data
	}
	verifyHistory := func(values []float64) {
		t.Helper()
		points, err := db.computeReadinessHistoryAt(ctx, 1, anchor)
		if err != nil || len(points) != 1 || points[0].Date != anchor {
			t.Fatalf("historical reader: %v, %v", points, err)
		}
		want := health.ComputeReadinessScore(values, nil, nil)
		if points[0].Score != want {
			t.Fatalf("historical score = %d, want %d for %v", points[0].Score, want, values)
		}
	}
	// When invoked by the dbt runner, capture the same database state and
	// compare the model's selected values directly to the production readers.
	// Roll back each capture so other cases in the runner remain independent.
	compareModel := func(reader string, expected []float64, present bool) {
		t.Helper()
		if os.Getenv("READINESS_HRV_SHADOW_COMPARE") != "1" {
			return
		}
		tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		path := "history"
		if reader == "morning" {
			path = "morning_warm"
		}
		_, err = tx.Exec(ctx, `INSERT INTO source_data.readiness_hrv_snapshot_inputs
			(snapshot_id,profile,target_date,reader,reader_path,snapshot_state,evidence_present,
			observed_date,raw_ms,cached_ms,repair_pending,cache_row_eligible)
			SELECT 'storage-readers','storage-fixture',$1,$2,$3,'captured',$4,days.d,
				raw.ms,cache.hrv_avg,repair.key IS NOT NULL,
				cache.hrv_avg IS NOT NULL OR cache.sleep_total IS NOT NULL OR cache.steps IS NOT NULL
			FROM (SELECT ($1::text::date-n)::text AS d FROM generate_series(0,30) n) days
			LEFT JOIN daily_scores cache ON cache.date=days.d
			LEFT JOIN settings repair ON repair.key=$5 || days.d
			LEFT JOIN LATERAL (SELECT avg(qty) AS ms FROM metric_points
				WHERE metric_name='heart_rate_variability' AND substring(date,1,10)=days.d
				AND quality='ok' AND qty>0) raw ON true`, anchor, reader, path, present, hrvRepairKeyPrefix)
		if err != nil {
			t.Fatal(err)
		}
		var selected []byte
		var available bool
		err = tx.QueryRow(ctx, `SELECT input_values,available FROM dbt_output.semantic_readiness_hrv_component_v1
			WHERE snapshot_id='storage-readers' AND profile='storage-fixture' AND reader=$1`, reader).Scan(&selected, &available)
		if err != nil {
			t.Fatal(err)
		}
		var actual []float64
		if err := json.Unmarshal(selected, &actual); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(actual, expected) || available != (present && len(expected) >= 9) {
			t.Fatalf("%s: dbt inputs %v (available %t), Go reader inputs %v (present %t)", reader, actual, available, expected, present)
		}
	}
	morning := readMorning()
	if !slices.Equal(morning.HRV, []float64{60, 41, 42, 43, 45, 46, 47, 48, 49, 50}) ||
		!morning.ReadinessEvidence.HRV.Present || morning.ReadinessEvidence.HRV.SampleCount != 2 {
		t.Fatalf("morning input %v, evidence %+v", morning.HRV, morning.ReadinessEvidence.HRV)
	}
	history := []float64{60, 51, 52, 53, 55, 56, 57, 58, 59, 60}
	verifyHistory(history)
	compareModel("morning", morning.HRV, true)
	compareModel("history", history, true)
	exec(`UPDATE metric_points SET qty=40 WHERE metric_name='heart_rate_variability' AND source='ring'`)
	history[0] = 45
	verifyHistory(history)
	corrected := readMorning()
	compareModel("morning", corrected.HRV, true)
	compareModel("history", history, true)
	if corrected.HRV[0] != 45 || morning.HRV[0] != 60 {
		t.Fatal("corrected input and captured morning input must retain their own values")
	}

	exec(`UPDATE metric_points SET quality='invalid' WHERE metric_name='heart_rate_variability' AND substring(date,1,10)=$1`, anchor)
	verifyHistory(nil)
	fallback := readMorning()
	compareModel("history", nil, false)
	compareModel("morning", fallback.HRV, true)
	if fallback.HRV[0] != 40 || !fallback.ReadinessEvidence.HRV.Present || fallback.ReadinessEvidence.HRV.SampleCount != 0 {
		t.Fatalf("same-day cache fallback: %v, %+v", fallback.HRV, fallback.ReadinessEvidence.HRV)
	}
	exec(`INSERT INTO settings(key,value) VALUES ($1,'true')`, hrvRepairKeyPrefix+anchor)
	withdrawn := readMorning()
	compareModel("morning", withdrawn.HRV, false)
	if withdrawn.ReadinessEvidence.HRV.Present || health.ComputeReadinessBriefing(*withdrawn, "en").ReadinessRawScore != 70 {
		t.Fatalf("prior HRV must not contribute on the withdrawn date: %+v", withdrawn.ReadinessEvidence.HRV)
	}
}
