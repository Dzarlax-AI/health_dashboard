// Unit-level test for the baseline-null-reason classifier. Integration
// tests at the bottom of the file additionally cover the DB round-trip
// of a valid nil+reason row and the verify-schema check.
//
// This file nails:
//
//  1. the per-baseline earliest-offset semantics — explicitly include
//     the EWMA case (lookback = max(N*3, 90), NOT N), which was the
//     original off-by-axis bug;
//  2. the strictly-before-t event_base_rate case (earliest = t-N), which
//     is one day further back than the inclusive [t-N+1..t] case;
//  3. the joint-state guard in SaveNaiveBaseline (reason set on a
//     non-nil value must return an error).

package storage

import (
	"testing"
	"time"
)

func TestEwmaLookbackDays(t *testing.T) {
	cases := []struct {
		windowN int
		want    int
	}{
		{45, 135}, // 45*3 = 135 > 90 — adaptive EWMA the writers use
		{30, 90},  // 30*3 = 90, equal floor
		{10, 90},  // 10*3 = 30, floored to 90
		{0, 90},   // edge — formula floor still applies
		{180, 540},
	}
	for _, tc := range cases {
		if got := ewmaLookbackDays(tc.windowN); got != tc.want {
			t.Errorf("ewmaLookbackDays(%d) = %d, want %d", tc.windowN, got, tc.want)
		}
	}
}

func TestClassifyBaselineNullReason(t *testing.T) {
	// Anchor day: 2026-05-16.
	anchor := time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name                string
		earliestOffsetDays  int
		epochStart          string
		wantReason          string
	}{
		// --- no epoch ---
		{
			name:               "no epoch set means warmup",
			earliestOffsetDays: 6,
			epochStart:         "",
			wantReason:         BaselineReasonWarmup,
		},

		// --- rolling_7d_mean: earliest = t-6 ---
		{
			name:               "rolling7d: epoch starts before earliest day means warmup",
			earliestOffsetDays: 6,
			epochStart:         "2026-05-01",
			wantReason:         BaselineReasonWarmup,
		},
		{
			name:               "rolling7d: epoch equals earliest day still means warmup",
			earliestOffsetDays: 6,
			epochStart:         "2026-05-10", // earliest = t-6 = 2026-05-10
			wantReason:         BaselineReasonWarmup,
		},
		{
			name:               "rolling7d: epoch starts after earliest day means source_epoch_boundary",
			earliestOffsetDays: 6,
			epochStart:         "2026-05-11", // earliest 2026-05-10 < 2026-05-11
			wantReason:         BaselineReasonSourceEpochBoundary,
		},

		// --- persistence_yesterday: earliest = t itself ---
		{
			name:               "persistence: epoch on candidate day means warmup",
			earliestOffsetDays: 0,
			epochStart:         "2026-05-16",
			wantReason:         BaselineReasonWarmup,
		},
		{
			name:               "persistence: epoch tomorrow means source_epoch_boundary",
			earliestOffsetDays: 0,
			epochStart:         "2026-05-17",
			wantReason:         BaselineReasonSourceEpochBoundary,
		},

		// --- ewma_45d: earliest = t - ewmaLookbackDays(45) = t - 135 ---
		{
			name:               "ewma45: epoch 1 day before window-start means source_epoch_boundary (regression test)",
			earliestOffsetDays: 135, // ewmaLookbackDays(45)
			epochStart:         "2026-02-01",
			// t-135 = 2026-01-01 < 2026-02-01 → boundary. The previous
			// classifier passed 45 here and incorrectly returned warmup
			// because it thought earliest = t-44 = 2026-04-02 > epoch.
			wantReason: BaselineReasonSourceEpochBoundary,
		},
		{
			name:               "ewma45: epoch before t-135 means warmup",
			earliestOffsetDays: 135,
			epochStart:         "2024-01-01",
			wantReason:         BaselineReasonWarmup,
		},
		{
			name:               "ewma45: epoch at exact t-135 (2026-01-01) still warmup",
			earliestOffsetDays: 135,
			epochStart:         "2026-01-01",
			wantReason:         BaselineReasonWarmup,
		},

		// --- event_base_rate strictly-before window: earliest = t-90 ---
		{
			name:               "event_base_rate: epoch 1 day before earliest means source_epoch_boundary (regression test)",
			earliestOffsetDays: 90,
			epochStart:         "2026-02-16", // t-90 = 2026-02-15, < epoch
			// Old classifier passed 90 but used inclusive semantics
			// (earliest = t-89), so earliest was 2026-02-16, equal to
			// epoch → warmup. New semantics: earliest = t-90 < epoch →
			// boundary. The chip now correctly says the prior window
			// has been clipped by the new epoch.
			wantReason: BaselineReasonSourceEpochBoundary,
		},
		{
			name:               "event_base_rate: epoch at exact t-90 (2026-02-15) means warmup",
			earliestOffsetDays: 90,
			epochStart:         "2026-02-15",
			wantReason:         BaselineReasonWarmup,
		},

		// --- guards ---
		{
			name:               "negative offset clamped to 0 (defensive)",
			earliestOffsetDays: -3,
			epochStart:         "2026-05-17",
			wantReason:         BaselineReasonSourceEpochBoundary,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyBaselineNullReason(anchor, tc.earliestOffsetDays, tc.epochStart)
			if got != tc.wantReason {
				t.Errorf("classifyBaselineNullReason(offset=%d, epochStart=%q) = %q, want %q",
					tc.earliestOffsetDays, tc.epochStart, got, tc.wantReason)
			}
		})
	}
}

// SaveNaiveBaseline's joint-state guard rejects every combination
// that would leave the chip's `unknown` state ambiguous. All four
// rejection paths are pure validation logic (no DB round-trip) —
// `db.pool` is nil and we expect to error before any SQL fires.
func TestSaveNaiveBaseline_JointStateGuard(t *testing.T) {
	v := 0.5
	base := NaiveBaseline{
		Date:           "2026-05-16",
		SubScore:       SubScoreRecoveryStability,
		TargetKind:     TargetKindRolling3d,
		BaselineKind:   BaselineKindEWMA45d,
		SourceEpoch:    InitialSourceEpoch,
		FormulaVersion: 1,
	}
	cases := []struct {
		name       string
		mutate     func(*NaiveBaseline)
		wantSubstr string
	}{
		{
			name: "value set, reason set — rejects (existing rule)",
			mutate: func(nb *NaiveBaseline) {
				nb.PredictedValue = &v
				nb.Reason = BaselineReasonWarmup
			},
			wantSubstr: "reason \"baseline_warmup\" set on non-nil",
		},
		{
			name: "value nil, reason empty — rejects (chip would lack explanation)",
			mutate: func(nb *NaiveBaseline) {
				nb.PredictedValue = nil
				nb.Reason = ""
			},
			wantSubstr: "reason must be set when predicted_value is nil",
		},
		{
			name: "value nil, reason unknown enum — rejects",
			mutate: func(nb *NaiveBaseline) {
				nb.PredictedValue = nil
				nb.Reason = "something_freeform"
			},
			wantSubstr: "invalid reason",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			nb := base
			tc.mutate(&nb)
			db := &DB{}
			err := db.SaveNaiveBaseline(nb)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q should contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

// Integration: SaveNaiveBaseline accepts the valid nil-value + valid
// reason path and the row round-trips through Postgres with `reason`
// populated. Complements TestSaveNaiveBaseline_JointStateGuard's
// pure-validation rejections.
func TestSaveNaiveBaseline_NilValueWithValidReasonPersists(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()

	err := db.SaveNaiveBaseline(NaiveBaseline{
		Date:           "2026-05-16",
		SubScore:       SubScoreRecoveryStability,
		TargetKind:     TargetKindRolling3d,
		BaselineKind:   BaselineKindEWMA45d,
		PredictedValue: nil,
		Reason:         BaselineReasonSourceEpochBoundary,
		SourceEpoch:    InitialSourceEpoch,
		FormulaVersion: 1,
	})
	if err != nil {
		t.Fatalf("SaveNaiveBaseline: %v", err)
	}

	var val *float64
	var reason *string
	if err := db.pool.QueryRow(t.Context(), `
		SELECT predicted_value, reason
		  FROM naive_baselines
		 WHERE date = $1 AND sub_score = $2 AND target_kind = $3 AND baseline_kind = $4
	`, "2026-05-16", SubScoreRecoveryStability, TargetKindRolling3d, BaselineKindEWMA45d).Scan(&val, &reason); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if val != nil {
		t.Errorf("predicted_value = %v, want nil", *val)
	}
	if reason == nil || *reason != BaselineReasonSourceEpochBoundary {
		t.Errorf("reason = %v, want %q", reason, BaselineReasonSourceEpochBoundary)
	}
}

// Integration: VerifyReadinessRedesignSchema must fail on the full
// contract for the reason column — missing, drifted to a non-TEXT
// type, or drifted to NOT NULL. Without these checks a failed ALTER
// (or a manual ops mistake on a tenant DB) would only surface as a
// SaveNaiveBaseline 500 at write time instead of a startup health
// failure. Each subtest mutates the column then asserts verify
// errors with the right marker.
func TestVerifyReadinessRedesignSchema_DetectsReasonColumnDrift(t *testing.T) {
	cases := []struct {
		name       string
		mutate     string
		wantSubstr string
	}{
		{
			name:       "column missing",
			mutate:     "ALTER TABLE naive_baselines DROP COLUMN reason",
			wantSubstr: "is missing",
		},
		{
			name: "column drifted to non-TEXT (INTEGER)",
			// USING NULL — every reason cell is NULL in a fresh schema, so
			// the cast can't lose data here. Cast to INTEGER so data_type
			// no longer matches "text".
			mutate:     "ALTER TABLE naive_baselines ALTER COLUMN reason TYPE INTEGER USING NULL",
			wantSubstr: "data_type",
		},
		{
			name: "column drifted to NOT NULL",
			// First seed a non-null row so the NOT NULL ALTER succeeds.
			// We piggy-back on the existing test by writing one explicit
			// reason row, then flipping the column constraint.
			mutate: `INSERT INTO naive_baselines
					(date, sub_score, target_kind, baseline_kind, predicted_value,
					 reason, source_epoch, formula_version)
				 VALUES
					('2026-05-16', 'recovery_stability', 'rolling_3d', 'ewma_45d',
					 NULL, 'baseline_warmup', 'initial', 1);
				 ALTER TABLE naive_baselines ALTER COLUMN reason SET NOT NULL`,
			wantSubstr: "is_nullable",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, cleanup := testDB(t)
			defer cleanup()

			if err := db.VerifyReadinessRedesignSchema(); err != nil {
				t.Fatalf("baseline verify (pristine schema) failed: %v", err)
			}

			if _, err := db.pool.Exec(t.Context(), tc.mutate); err != nil {
				t.Fatalf("apply drift mutation: %v", err)
			}

			err := db.VerifyReadinessRedesignSchema()
			if err == nil {
				t.Fatal("expected verify error after drift, got nil")
			}
			if !contains(err.Error(), "naive_baselines.reason") {
				t.Errorf("verify error should mention naive_baselines.reason, got: %v", err)
			}
			if !contains(err.Error(), tc.wantSubstr) {
				t.Errorf("verify error should mention %q, got: %v", tc.wantSubstr, err)
			}
		})
	}
}
