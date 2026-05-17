// Regression test for the threshold echo epoch scoping fix.
//
// The wizard's Step 5 compares the echoed thresholds against the
// effective ChronicLoadConfig. Without epoch scoping, a fresh
// onboarding (current epoch has no chronic rows yet) would silently
// surface a row from an older epoch — either a false OK or a false
// mismatch. The test plants chronic rows in an old epoch only,
// asks for the echo of the current epoch, and expects "nothing yet".

package storage

import (
	"context"
	"testing"
	"time"
)

func TestLoadChronicThresholdEcho_EpochScoped(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	// Seed one eligible chronic_label row in the OLD epoch with a
	// data_coverage payload that echoes thresholds the wizard would
	// happily surface if the query were unscoped.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const oldEpoch = "epoch-old"
	const newEpoch = "epoch-new"
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO source_epochs (epoch_id, kind, start_date, end_date, detected_by)
		VALUES ($1, $2, '2024-01-01', '2024-12-31', $4),
		       ($3, $2, '2025-01-01', NULL, $4)
		ON CONFLICT (epoch_id) DO NOTHING
	`, oldEpoch, SourceEpochKindPhysiology, newEpoch, DetectedByManual); err != nil {
		t.Fatalf("seed epochs: %v", err)
	}
	if _, err := db.pool.Exec(ctx, `
		INSERT INTO target_snapshots
			(date, sub_score, target_kind, target_value, eligible,
			 eligibility_reason, source_epoch, formula_version,
			 data_coverage, computed_at)
		VALUES ('2024-06-01', $1, $2, 0, TRUE, 'ok', $3, 1,
		        '{"breach_threshold": 5, "acute_density_threshold": 7}'::jsonb,
		        NOW())
	`, SubScoreChronicLoad, TargetKindChronicLabel, oldEpoch); err != nil {
		t.Fatalf("seed old-epoch chronic row: %v", err)
	}

	// Asking for the OLD epoch returns the seeded echo.
	echo, err := db.LoadChronicThresholdEcho(oldEpoch)
	if err != nil {
		t.Fatalf("load echo (old epoch): %v", err)
	}
	if echo == nil {
		t.Fatal("expected echo for old epoch, got nil")
	}
	if echo.BreachThreshold != 5 || echo.AcuteDensityThresh != 7 {
		t.Errorf("echo = %+v, want breach=5 acute_density=7", echo)
	}

	// Asking for the NEW (current) epoch returns nil — no chronic
	// rows written there yet. This is the load-bearing assertion:
	// without epoch scoping the query would fall back to the old
	// row and the wizard would render a misleading comparison.
	echo, err = db.LoadChronicThresholdEcho(newEpoch)
	if err != nil {
		t.Fatalf("load echo (new epoch): %v", err)
	}
	if echo != nil {
		t.Errorf("expected nil echo for new epoch (no chronic rows yet), got %+v", echo)
	}
}
