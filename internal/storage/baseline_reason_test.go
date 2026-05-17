// Unit-level test for the baseline-null-reason classifier. Integration
// tests downstream prove the writers wire it in; this file nails:
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

func TestSaveNaiveBaseline_RejectsReasonOnNonNilValue(t *testing.T) {
	v := 0.5
	db := &DB{}
	err := db.SaveNaiveBaseline(NaiveBaseline{
		Date:           "2026-05-16",
		SubScore:       SubScoreRecoveryStability,
		TargetKind:     TargetKindRolling3d,
		BaselineKind:   BaselineKindEWMA45d,
		PredictedValue: &v,
		Reason:         BaselineReasonWarmup,
		SourceEpoch:    InitialSourceEpoch,
		FormulaVersion: 1,
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "reason") {
		t.Errorf("error message should mention 'reason', got: %v", err)
	}
}
