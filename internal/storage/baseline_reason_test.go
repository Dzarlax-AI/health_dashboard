// Unit-level test for the baseline-null-reason classifier. The
// integration tests downstream prove the writers wire it in; this
// file just nails the decision logic.

package storage

import (
	"testing"
	"time"
)

func TestClassifyBaselineNullReason(t *testing.T) {
	// Anchor day: 2026-05-16.
	anchor := time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC)

	cases := []struct {
		name        string
		windowDays  int
		epochStart  string
		wantReason  string
	}{
		{
			name:       "no epoch set means warmup",
			windowDays: 7,
			epochStart: "",
			wantReason: BaselineReasonWarmup,
		},
		{
			name:       "epoch starts before window earliest day means warmup",
			windowDays: 7,
			epochStart: "2026-05-01", // window earliest = 2026-05-10, post-epoch
			wantReason: BaselineReasonWarmup,
		},
		{
			name:       "epoch starts at window earliest day still means warmup",
			windowDays: 7,
			epochStart: "2026-05-10", // window earliest = 2026-05-10, equal
			wantReason: BaselineReasonWarmup,
		},
		{
			name:       "epoch starts inside window means source_epoch_boundary",
			windowDays: 7,
			epochStart: "2026-05-12", // window = 2026-05-10..16; epochStart clips earliest 2 days
			wantReason: BaselineReasonSourceEpochBoundary,
		},
		{
			name:       "epoch starts after candidate day still means source_epoch_boundary",
			windowDays: 7,
			epochStart: "2026-06-01",
			wantReason: BaselineReasonSourceEpochBoundary,
		},
		{
			name:       "persistence_yesterday window=1 with epoch tomorrow boundaries",
			windowDays: 1,
			epochStart: "2026-05-17", // window = {2026-05-16}, epochStart > candidate
			wantReason: BaselineReasonSourceEpochBoundary,
		},
		{
			name:       "persistence_yesterday window=1 with epoch on candidate day means warmup",
			windowDays: 1,
			epochStart: "2026-05-16", // window = {2026-05-16}, epochStart matches
			wantReason: BaselineReasonWarmup,
		},
		{
			name:       "ewma_45d window with epoch deep in window",
			windowDays: 45,
			epochStart: "2026-05-01", // 45-day window earliest = 2026-04-02
			wantReason: BaselineReasonSourceEpochBoundary,
		},
		{
			name:       "ewma_45d window with epoch well before window means warmup",
			windowDays: 45,
			epochStart: "2024-01-01",
			wantReason: BaselineReasonWarmup,
		},
		{
			name:       "zero windowDays clamped to 1",
			windowDays: 0,
			epochStart: "2026-05-17",
			wantReason: BaselineReasonSourceEpochBoundary,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyBaselineNullReason(anchor, tc.windowDays, tc.epochStart)
			if got != tc.wantReason {
				t.Errorf("classifyBaselineNullReason(window=%d, epochStart=%q) = %q, want %q",
					tc.windowDays, tc.epochStart, got, tc.wantReason)
			}
		})
	}
}

// SaveNaiveBaseline rejects rows where a reason is set but the value
// is non-nil — this catches a future writer regression where both
// fields would silently disagree. Pure validation logic, no DB round
// trip.
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
