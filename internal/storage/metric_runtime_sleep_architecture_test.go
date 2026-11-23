package storage

import (
	"os"
	"testing"
)

func TestMetricRuntimeSleepArchitectureCapture(t *testing.T) {
	dir := os.Getenv("HEALTH_METRIC_AUDIT_DIR")
	if dir == "" || os.Getenv("SLEEP_DOCKER_TOKEN") == "" {
		t.Skip("requires synthetic sleep fixture and audit directory")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	date := "2026-03-03"
	record := insertTestRawRecord(t, db, "runtime-architecture")
	_, err := db.pool.Exec(ctx, `insert into metric_points(health_record_id,metric_name,date,qty,source,quality) values ($1,'sleep_core','2026-03-02 23:00:00 +0000',7,'Synthetic Watch','ok')`, record)
	if err != nil {
		t.Fatal(err)
	}
	days, err := db.LoadSleepArchitectureDays(date, date)
	if err != nil {
		t.Fatal(err)
	}
	day, ok := days[date]
	if !ok {
		t.Fatal("missing synthetic architecture day")
	}
	rows, err := db.LoadSleepArchitectureFeatures(date, date)
	if err != nil {
		t.Fatal(err)
	}
	features := rows[date]
	writeMetricRuntimeCapture(t, dir, "sleep_architecture.json", metricRuntimeCapture{"sleep_architecture", "storage.LoadSleepArchitectureDays", day, map[string]any{"date": date}}, metricRuntimeCapture{"sleep_architecture_features", "storage.LoadSleepArchitectureFeatures", features, map[string]any{"date": date}})
}
