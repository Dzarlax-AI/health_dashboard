// Bulk seed helpers for integration tests.
//
// The production writers (SaveNaiveBaseline, SaveTargetSnapshot,
// etc.) intentionally insert one row at a time so every write
// passes the joint-state validation. Integration tests that build
// up 120+ days of seed data ahead of running a backfill were paying
// that round-trip cost per row — at ~50ms RTT to the test Postgres
// over Tailscale, 240 rows = ~12s of test time spent on network,
// not logic. The four chip-calibration / acute-risk integration
// tests were each spending 15–20s mostly in seed loops.
//
// These helpers batch the same INSERT semantics (ON CONFLICT DO
// UPDATE on the row's PK) into a single multi-VALUES statement, so
// a 240-row seed becomes one round-trip. Tests that construct
// known-valid rows can use them directly; tests exercising the
// joint-state validation itself still go through the per-row
// Save... methods.
//
// Joint-state guarantees baked in:
//   - seedNaiveBaselinesBulk normalises Reason="" to SQL NULL so
//     the ON CONFLICT clause produces the same shape SaveNaiveBaseline
//     would.
//   - All other fields are pass-through; the caller is responsible
//     for producing rows that match the production invariants.

package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// seedNaiveBaselinesBulk upserts many naive_baselines rows in a
// single INSERT. Returns immediately on an empty slice. Same
// conflict-resolution rule as SaveNaiveBaseline (PK is the four-
// column composite).
func seedNaiveBaselinesBulk(t *testing.T, db *DB, rows []NaiveBaseline) {
	t.Helper()
	if len(rows) == 0 {
		return
	}
	const colsPerRow = 8
	var sb strings.Builder
	sb.WriteString(`INSERT INTO naive_baselines
		(date, sub_score, target_kind, baseline_kind, predicted_value,
		 reason, source_epoch, formula_version, computed_at) VALUES `)
	args := make([]any, 0, len(rows)*colsPerRow)
	for i, r := range rows {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := i * colsPerRow
		fmt.Fprintf(&sb, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, NOW())",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8)
		var reason any
		if r.Reason != "" {
			reason = r.Reason
		}
		args = append(args, r.Date, r.SubScore, r.TargetKind, r.BaselineKind,
			r.PredictedValue, reason, r.SourceEpoch, r.FormulaVersion)
	}
	sb.WriteString(` ON CONFLICT (date, sub_score, target_kind, baseline_kind) DO UPDATE SET
		predicted_value = excluded.predicted_value,
		reason = excluded.reason,
		source_epoch = excluded.source_epoch,
		formula_version = excluded.formula_version,
		computed_at = NOW()`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.pool.Exec(ctx, sb.String(), args...); err != nil {
		t.Fatalf("seedNaiveBaselinesBulk (%d rows): %v", len(rows), err)
	}
}

// targetSnapshotSeed mirrors the columns a test seed cares about
// for target_snapshots. Smaller than the production TargetSnapshot
// struct because tests rarely touch data_coverage on seeds.
type targetSnapshotSeed struct {
	Date              string
	SubScore          string
	TargetKind        string
	TargetValue       *float64 // nil → SQL NULL
	Eligible          bool
	EligibilityReason string
	SourceEpoch       string
	FormulaVersion    int
}

// seedTargetSnapshotsBulk upserts many target_snapshots rows in a
// single INSERT. Uses the same PK conflict rule as
// SaveTargetSnapshot.
func seedTargetSnapshotsBulk(t *testing.T, db *DB, rows []targetSnapshotSeed) {
	t.Helper()
	if len(rows) == 0 {
		return
	}
	const colsPerRow = 8
	var sb strings.Builder
	sb.WriteString(`INSERT INTO target_snapshots
		(date, sub_score, target_kind, target_value, eligible,
		 eligibility_reason, source_epoch, formula_version, computed_at) VALUES `)
	args := make([]any, 0, len(rows)*colsPerRow)
	for i, r := range rows {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := i * colsPerRow
		fmt.Fprintf(&sb, "($%d, $%d, $%d, $%d, $%d, $%d, $%d, $%d, NOW())",
			base+1, base+2, base+3, base+4, base+5, base+6, base+7, base+8)
		args = append(args, r.Date, r.SubScore, r.TargetKind, r.TargetValue,
			r.Eligible, r.EligibilityReason, r.SourceEpoch, r.FormulaVersion)
	}
	sb.WriteString(` ON CONFLICT (date, sub_score, target_kind) DO UPDATE SET
		target_value = excluded.target_value,
		eligible = excluded.eligible,
		eligibility_reason = excluded.eligibility_reason,
		source_epoch = excluded.source_epoch,
		formula_version = excluded.formula_version,
		computed_at = NOW()`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.pool.Exec(ctx, sb.String(), args...); err != nil {
		t.Fatalf("seedTargetSnapshotsBulk (%d rows): %v", len(rows), err)
	}
}

// seedAutonomicRowsBulk upserts many daily_scores rows with paired
// HRV / RHR data. Used by Acute Risk tests that build up multi-month
// history before exercising a single backfill.
type autonomicSeed struct {
	Date string
	HRV  *float64
	RHR  *float64
}

func seedAutonomicRowsBulk(t *testing.T, db *DB, rows []autonomicSeed) {
	t.Helper()
	if len(rows) == 0 {
		return
	}
	const colsPerRow = 3
	var sb strings.Builder
	sb.WriteString(`INSERT INTO daily_scores (date, hrv_avg, rhr_avg) VALUES `)
	args := make([]any, 0, len(rows)*colsPerRow)
	for i, r := range rows {
		if i > 0 {
			sb.WriteString(", ")
		}
		base := i * colsPerRow
		fmt.Fprintf(&sb, "($%d, $%d, $%d)", base+1, base+2, base+3)
		args = append(args, r.Date, r.HRV, r.RHR)
	}
	sb.WriteString(` ON CONFLICT (date) DO UPDATE SET
		hrv_avg = excluded.hrv_avg,
		rhr_avg = excluded.rhr_avg`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := db.pool.Exec(ctx, sb.String(), args...); err != nil {
		t.Fatalf("seedAutonomicRowsBulk (%d rows): %v", len(rows), err)
	}
}
