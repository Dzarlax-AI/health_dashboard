package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"health-receiver/internal/ctxdb"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
	"health-receiver/internal/testdb"
)

func TestWorkoutZoneIngestSQL(t *testing.T) {
	ctx := context.Background()
	dsn := testdb.DSN(t)
	admin, err := testdb.NewPool(ctx, dsn, "")
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := testdb.SchemaName("workout_zone")
	if err := testdb.CreateSchema(ctx, admin, schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := testdb.DropSchema(ctx, admin, schema); err != nil {
			t.Error(err)
		}
	}()
	pool, err := testdb.NewPool(ctx, dsn, schema)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := storage.NewFromPool(pool)
	if err := db.EnsureAllTables(); err != nil {
		t.Fatal(err)
	}
	zones, err := health.ParseHRZones("100,120,140,160")
	if err != nil {
		t.Fatal(err)
	}
	input := haeWorkout{ID: "local", Start: "2000-01-01 00:00:00 +0000", End: "2000-01-01 00:08:00 +0000"}
	check := func(z health.HRZones, want *[5]int) {
		t.Helper()
		row, err := convertHAEWorkout(input, z, db.WorkoutZoneSeconds)
		if err != nil {
			t.Fatal(err)
		}
		for i, got := range []*int{row.HRZ1Sec, row.HRZ2Sec, row.HRZ3Sec, row.HRZ4Sec, row.HRZ5Sec} {
			if want == nil && got != nil || want != nil && (got == nil || *got != want[i]) {
				t.Fatalf("zone %d got %v, want %v", i+1, got, want)
			}
		}
	}
	check(zones, nil)
	input.HeartRateData = []hrSample{{Date: "unparseable"}}
	check(zones, &[5]int{})
	// First interval rounds down just below half a second, the second up.
	// The first tied sample contributes zero. Cap the long gap at 300s.
	input.HeartRateData = append(input.HeartRateData,
		hrSample{Date: "2000-01-01 00:00:01 +0000", Avg: 120},
		hrSample{Date: "2000-01-01 00:00:00 +0000", Avg: 90},
		hrSample{Date: "2000-01-01 00:00:00.499999999 +0000", Avg: 100},
		hrSample{Date: "2000-01-01 00:00:01 +0000", Avg: 140},
		hrSample{Date: "2000-01-01 00:07:59 +0000", Avg: 160})
	check(zones, &[5]int{0, 1, 0, 300, 1})
	check(health.HRZones{}, nil)
	input.End = "2000-01-01 00:07:58 +0000"
	check(zones, &[5]int{0, 1, 0, 300, 0})

	payload, err := json.Marshal([]haeWorkout{input})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/health/workouts", bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	(&Handler{hrZones: zones}).workouts(rec, req.WithContext(ctxdb.WithDB(req.Context(), db, schema)))
	var response struct{ Ingested, Failed int }
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK || response.Ingested != 1 || response.Failed != 0 {
		t.Fatalf("ingest status=%d response=%+v err=%v", rec.Code, response, err)
	}
	var persisted [5]int
	err = pool.QueryRow(ctx, `SELECT array[hr_z1_sec,hr_z2_sec,hr_z3_sec,hr_z4_sec,hr_z5_sec] FROM workouts WHERE external_id=$1`, input.ID).Scan(&persisted)
	if err != nil || persisted != [5]int{0, 1, 0, 300, 0} {
		t.Fatalf("saved zone seconds %v, err %v", persisted, err)
	}

}
