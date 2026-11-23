package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestNamedContractRemainingCapture uses only a disposable testDB schema.
func TestNamedContractRemainingCapture(t *testing.T) {
	dir := os.Getenv("HEALTH_METRIC_AUDIT_DIR")
	if dir == "" {
		t.Skip("synthetic audit output not requested")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := context.Background()
	record := insertTestRawRecord(t, db, "named-contract-capture")
	for _, p := range []struct {
		metric, date string
		qty          float64
	}{
		{"sleep_core", "2026-08-05 00:30:00 +0000", 4}, {"sleep_rem", "2026-08-05 04:30:00 +0000", 2}, {"sleep_awake", "2026-08-05 06:30:00 +0000", .5},
	} {
		if _, err := db.pool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,quality) VALUES($1,$2,'hr',$3,$4,'synthetic','ok')`, record, p.metric, p.date, p.qty); err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.BackfillWakeTimes("2026-08-05", "2026-08-05", time.UTC, false)
	if err != nil || result.Written != 1 {
		t.Fatalf("wake backfill: %+v %v", result, err)
	}
	wake, err := db.GetDerivedMetric(DerivedMetricWakeTime, "2026-08-05")
	if err != nil {
		t.Fatal(err)
	}
	if wake == nil || wake.ValueTimestamp == nil || !wake.ValueTimestamp.Equal(time.Date(2026, 8, 5, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("unexpected wake: %+v", wake)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO metric_points(health_record_id,metric_name,units,date,qty,source,quality)
 SELECT $1,'heart_rate_variability','ms',to_char('2026-08-05'::date-n,'YYYY-MM-DD')||' 02:00:00 +0000',50+n%5,'synthetic','ok' FROM generate_series(1,20) n`, record); err != nil {
		t.Fatal(err)
	}
	baseline, ok, err := db.PersonalBaselineContext(ctx, "2026-08-05", ChannelHRV, 30, time.UTC)
	if err != nil || !ok || baseline.SampleCount != 20 {
		t.Fatalf("HRV baseline: %+v ok=%v err=%v", baseline, ok, err)
	}
	if _, err := db.pool.Exec(ctx, `INSERT INTO daily_scores(date,sleep_total,sleep_deep,sleep_rem,sleep_awake,calories) SELECT ('2026-08-05'::date-n)::text,7,1,1.5,0.3,350 FROM generate_series(0,20) n`); err != nil {
		t.Fatal(err)
	}
	progress, err := db.BackfillEnergyRange(ctx, "UTC", "2026-08-05", "2026-08-05", false, nil)
	if err != nil || progress.OK != 1 {
		t.Fatalf("energy backfill: %+v %v", progress, err)
	}
	var energy []byte
	if err := db.pool.QueryRow(ctx, `SELECT row_to_json(e) FROM (SELECT bank,drain_delta,restore_delta,formula_version,flags FROM energy_snapshots WHERE date='2026-08-05') e`).Scan(&energy); err != nil {
		t.Fatal(err)
	}
	var energyValue map[string]any
	if err := json.Unmarshal(energy, &energyValue); err != nil {
		t.Fatal(err)
	}
	rows := []map[string]any{
		{"metric_id": "wake_time", "key": map[string]any{"date": "2026-08-05"}, "value": wake.ValueTimestamp, "source": "storage.BackfillWakeTimes/GetDerivedMetric (historical provisional)", "profile": "synthetic"},
		{"metric_id": "hrv_fixed_window_baseline_v1", "key": map[string]any{"date": "2026-08-05", "window_days": 30}, "value": baseline.Median, "evidence": map[string]any{"mad_sd": baseline.MADSD, "sample_count": baseline.SampleCount, "state": baseline.State}, "source": "storage.PersonalBaselineContext(ChannelHRV)", "profile": "synthetic"},
		{"metric_id": "energy_eod_backfill", "key": map[string]any{"date": "2026-08-05"}, "value": energyValue["bank"], "evidence": energyValue, "source": "storage.BackfillEnergyRange/energy_snapshots", "profile": "synthetic"},
	}
	b, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "remaining.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
}
