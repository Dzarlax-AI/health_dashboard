package storage

import (
	"context"
	"testing"
	"time"
)

func TestComputeStressObservabilitySummary_ReadOnlyAndEffectiveBetaGated(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	if err := db.SaveSettings(map[string]string{
		"energy.beta":                 "0.8",
		"energy.z_threshold":          "0.5",
		"energy.stress_drain_enabled": "false",
	}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	seedStressDailyScore(t, db, "2026-05-28", 1.5, []string{"sustained_load"})
	seedStressDailyScore(t, db, "2026-05-29", 0, []string{"stale_stress"})

	beforeCount, beforeMaxUpdated := settingsWriteFingerprint(t, db)
	got, err := db.ComputeStressObservabilitySummary(context.Background(), "UTC", "2026-05-30", 7)
	if err != nil {
		t.Fatalf("ComputeStressObservabilitySummary: %v", err)
	}
	afterCount, afterMaxUpdated := settingsWriteFingerprint(t, db)
	if beforeCount != afterCount || beforeMaxUpdated != afterMaxUpdated {
		t.Fatalf("summary wrote settings: before=(%d,%s) after=(%d,%s)",
			beforeCount, beforeMaxUpdated, afterCount, afterMaxUpdated)
	}
	if got.EffectiveBeta != 0 || got.Applied {
		t.Fatalf("EffectiveBeta=%v Applied=%v, want observed-only beta gate", got.EffectiveBeta, got.Applied)
	}
	if got.Mode != "observed_only" {
		t.Fatalf("Mode = %q, want observed_only", got.Mode)
	}
	if got.Distribution.Days != 2 {
		t.Fatalf("Distribution.Days = %d, want 2", got.Distribution.Days)
	}
	if got.Distribution.FlagCounts["sustained_load"] != 1 || got.Distribution.FlagCounts["stale_stress"] != 1 {
		t.Fatalf("FlagCounts = %+v, want sustained_load=1 stale_stress=1", got.Distribution.FlagCounts)
	}
}

func seedStressDailyScore(t *testing.T, db *DB, date string, load float64, flags []string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := db.pool.Exec(ctx, `
		INSERT INTO daily_scores (date, sustained_hr_load, stress_flags)
		VALUES ($1, $2, $3)
		ON CONFLICT (date) DO UPDATE SET
			sustained_hr_load = excluded.sustained_hr_load,
			stress_flags = excluded.stress_flags`,
		date, load, flags)
	if err != nil {
		t.Fatalf("seed daily_scores %s: %v", date, err)
	}
}

func settingsWriteFingerprint(t *testing.T, db *DB) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var count int
	var maxUpdated string
	if err := db.pool.QueryRow(ctx, `SELECT COUNT(*), COALESCE(MAX(updated_at), '') FROM settings`).Scan(&count, &maxUpdated); err != nil {
		t.Fatalf("settings fingerprint: %v", err)
	}
	return count, maxUpdated
}
