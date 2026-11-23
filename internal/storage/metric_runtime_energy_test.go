package storage

import (
	"os"
	"testing"
	"time"
)

func TestMetricRuntimeEnergyCapture(t *testing.T) {
	dir := os.Getenv("HEALTH_METRIC_AUDIT_DIR")
	if dir == "" || os.Getenv("ENERGY_BANK_PILOT") != "synthetic-only" {
		t.Skip("requires synthetic energy fixture and audit directory")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	date := time.Now().UTC().Format(time.DateOnly)
	_, err := db.pool.Exec(ctx, `INSERT INTO daily_scores(date,sleep_total,sleep_deep,sleep_rem,sleep_awake,calories,sustained_hr_load,stress_flags) SELECT ($1::date-n)::text,7.5,1.5,1.5,0.25,250,1.5,ARRAY[]::text[] FROM generate_series(0,20) n`, date)
	if err != nil {
		t.Fatal(err)
	}
	result, err := db.ComputeBankForDate(ctx, "UTC", date)
	if err != nil {
		t.Fatal(err)
	}
	writeMetricRuntimeCapture(t, dir, "energy.json", metricRuntimeCapture{"energy_state", "storage.ComputeBankForDate", result, map[string]any{"date": date}})
}
