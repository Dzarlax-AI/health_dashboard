package storage

import (
	"os"
	"testing"
	"time"
)

// Exports actual synthetic storage-reader results for the optional local metric audit.
func TestMetricRuntimeWorkoutCapture(t *testing.T) {
	dir := os.Getenv("HEALTH_METRIC_AUDIT_DIR")
	if dir == "" || os.Getenv("WORKOUT_STATS_PILOT") != "synthetic-only" {
		t.Skip("requires marked disposable workout pilot and audit directory")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	start := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC)
	end := start.Add(time.Hour)
	_, err := db.pool.Exec(ctx, `insert into workouts(external_id,name,start_time,end_time,duration_sec,avg_hr_bpm,max_hr_bpm,distance_km,energy_kcal,intensity,avg_speed_kmh,max_speed_kmh,elevation_up_m,step_count_total,step_cadence_spm,temperature_c,humidity_pct,hr_z1_sec,hr_z2_sec,hr_z3_sec,hr_z4_sec,hr_z5_sec) values('synthetic','Ride',$1,$2,3600,140,180,25,400,0.7,25,32,120,0,0,18,45,60,120,240,60,0)`, start, end)
	if err != nil {
		t.Fatal(err)
	}
	workouts, err := db.ListWorkouts(start, end, "")
	if err != nil || len(workouts) != 1 {
		t.Fatalf("workout reader: %v rows=%d", err, len(workouts))
	}
	stats, err := db.WorkoutStats(start, end, "")
	if err != nil {
		t.Fatal(err)
	}
	types, err := db.ListWorkoutTypes()
	if err != nil || len(types) != 1 {
		t.Fatalf("workout type reader: %v rows=%d", err, len(types))
	}
	writeMetricRuntimeCapture(t, dir, "workouts.json", metricRuntimeCapture{"workout", "storage.ListWorkouts", workouts[0], map[string]any{"external_id": workouts[0].ExternalID}}, metricRuntimeCapture{"workout_period", "storage.WorkoutStats", stats, map[string]any{"start": start, "end": end}}, metricRuntimeCapture{"workout_type", "storage.ListWorkoutTypes", types[0], map[string]any{"name": types[0].Name}})
}
