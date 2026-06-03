package storage

import (
	"context"
	"testing"
	"time"
)

func TestBuildIllnessEvidenceInput_UsesDailyRHRFallbackAndAutonomicPattern(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start, err := time.Parse(isoDate, "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 36; i++ {
		d := start.AddDate(0, 0, i).Format(isoDate)
		rhr := 60.0 + float64((i%5)-2)
		load := 0.2
		steps := 4000.0
		calories := 2100.0
		if d >= "2026-02-02" && d <= "2026-02-05" {
			load = 3.0
		}
		if d == "2026-02-05" {
			rhr = 70.0
		}
		_, err := db.pool.Exec(ctx, `
			INSERT INTO daily_scores (date, rhr_avg, steps, calories, sustained_hr_load)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (date) DO UPDATE SET
				rhr_avg = excluded.rhr_avg,
				steps = excluded.steps,
				calories = excluded.calories,
				sustained_hr_load = excluded.sustained_hr_load
		`, d, rhr, steps, calories, load)
		if err != nil {
			t.Fatalf("seed daily score %s: %v", d, err)
		}
	}

	in := db.BuildIllnessEvidenceInput("2026-02-05", nil)
	if in.RHR == nil || in.RHR.Status != "ok" {
		t.Fatalf("RHR evidence = %+v, want daily fallback ok", in.RHR)
	}
	if in.RHR.Method != "daily_scores_mean_std:rhr_avg" {
		t.Fatalf("RHR method = %q, want daily_scores fallback", in.RHR.Method)
	}
	if in.RHR.ZScore < 1.0 {
		t.Fatalf("RHR z = %.2f, want >= 1.0", in.RHR.ZScore)
	}
	if in.AutonomicPatternDays != 4 {
		t.Fatalf("AutonomicPatternDays = %d, want 4", in.AutonomicPatternDays)
	}
}
