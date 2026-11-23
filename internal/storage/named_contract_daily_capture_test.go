package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestNamedContractDailyCapture records values written by the live aggregation
// path using a disposable schema and synthetic points. Missing values remain null.
func TestNamedContractDailyCapture(t *testing.T) {
	out := os.Getenv("HEALTH_METRIC_AUDIT_DIR")
	if out == "" {
		t.Skip("synthetic audit output not requested")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	date := "2000-01-02"
	names := []string{"step_count", "active_energy", "apple_exercise_time", "heart_rate_variability", "resting_heart_rate", "respiratory_rate", "vo2_max", "blood_oxygen_saturation", "sleep_total", "sleep_deep", "sleep_rem", "sleep_core", "sleep_awake", "sleep_unspecified"}
	values := []float64{1000, 300, 30, 55, 60, 15, 42, 97, 8, 1, 2, 5, .2, 0}
	points := make([]MetricPoint, 0, len(names))
	for i, name := range names {
		points = append(points, MetricPoint{MetricName: name, Units: "synthetic", Date: date + " 08:00:00 +0000", Source: "synthetic", Qty: values[i]})
	}
	insertHRVRepairPoints(t, db, points)
	if err := db.UpsertRecentCache([]string{date}, false); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	err := db.pool.QueryRow(context.Background(), `SELECT row_to_json(d) FROM (SELECT steps, calories, exercise_min, hrv_avg, rhr_avg, resp_avg, vo2_avg, spo2_avg, sleep_deep, sleep_rem, sleep_core, sleep_awake, sleep_unspecified, sleep_total FROM daily_scores WHERE date=$1) d`, date).Scan(&payload)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]any
	if err := json.Unmarshal(payload, &row); err != nil {
		t.Fatal(err)
	}
	records := make([]map[string]any, 0, len(row))
	for field, value := range row {
		records = append(records, map[string]any{"metric_id": "daily." + field, "key": map[string]any{"date": date}, "value": value, "source": "storage.UpsertRecentCache/daily_scores", "profile": "synthetic"})
	}
	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, "daily.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}
