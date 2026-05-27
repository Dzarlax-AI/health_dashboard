package health

import (
	"math"
	"testing"
)

func fp(v float64) *float64 { return &v }

func TestComputeSleepEfficiency_NormalNight(t *testing.T) {
	r := SleepRow{
		Date:  "2026-05-15",
		Total: fp(7.5),
		Deep:  fp(1.5), REM: fp(1.8), Core: fp(4.2),
		Awake: fp(0.5),
	}
	got := ComputeSleepEfficiency(r)
	if !got.Eligible || got.EligibilityReason != SleepEligibilityOK {
		t.Fatalf("expected ok eligibility, got %+v", got)
	}
	if got.Efficiency == nil {
		t.Fatal("expected efficiency value, got nil")
	}
	want := 7.5 / (7.5 + 0.5) // 0.9375
	if math.Abs(*got.Efficiency-want) > 1e-9 {
		t.Errorf("efficiency = %v, want %v", *got.Efficiency, want)
	}
}

func TestComputeSleepEfficiency_AppleWatchStructuralZero(t *testing.T) {
	// Apple Watch night: staged components present, awake row absent
	// (filtered upstream by qty > 0), staged sums to within 2% of total.
	r := SleepRow{
		Date:  "2026-05-15",
		Total: fp(7.0),
		Deep:  fp(1.4), REM: fp(1.6), Core: fp(4.0),
		Awake: nil,
	}
	got := ComputeSleepEfficiency(r)
	if !got.Eligible || got.EligibilityReason != SleepEligibilityOKAwakeStructuralZero {
		t.Fatalf("expected structural-zero eligibility, got %+v", got)
	}
	if got.Efficiency == nil || *got.Efficiency != 1.0 {
		t.Errorf("expected efficiency 1.0 for structural zero, got %+v", got.Efficiency)
	}
}

func TestComputeSleepEfficiency_MissingAwakeUnknown(t *testing.T) {
	// Staged present, awake absent, but staged disagrees with total
	// well beyond the 2% tolerance — cannot promote to structural zero.
	r := SleepRow{
		Date:  "2026-05-15",
		Total: fp(8.0),
		Deep:  fp(1.0), REM: fp(1.0), Core: fp(2.0), // sum = 4.0, off by 50%
		Awake: nil,
	}
	got := ComputeSleepEfficiency(r)
	if got.Eligible || got.EligibilityReason != SleepEligibilityMissingAwakeUnknown {
		t.Fatalf("expected missing_awake_unknown, got %+v", got)
	}
	if got.Efficiency != nil {
		t.Error("expected nil efficiency when ineligible")
	}
}

func TestComputeSleepEfficiency_SleepTotalOutOfRange(t *testing.T) {
	// Present-but-implausible values. `nil total` lives in
	// TestComputeSleepEfficiency_SleepDataMissing now — formula_version 2
	// split the old over-broad reason into two distinct ones.
	cases := []struct {
		name  string
		total *float64
	}{
		{"zero total", fp(0)},
		{"negative total", fp(-1)},
		{"under floor", fp(3.5)},
		{"over ceiling", fp(15)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := SleepRow{
				Date:  "2026-05-15",
				Total: c.total,
				Deep:  fp(1.0), REM: fp(1.0), Core: fp(1.0),
				Awake: fp(0.3),
			}
			got := ComputeSleepEfficiency(r)
			if got.Eligible || got.EligibilityReason != SleepEligibilitySleepTotalOutOfRange {
				t.Fatalf("expected sleep_total_out_of_range for %s, got %+v", c.name, got)
			}
			if got.Efficiency != nil {
				t.Error("expected nil efficiency when ineligible")
			}
		})
	}
}

func TestComputeSleepEfficiency_SleepDataMissing(t *testing.T) {
	// Empirical case from the full-history backfill: source row entirely
	// absent for the date, all fields NULL. Should resolve to
	// sleep_data_missing — distinct from a present-but-short night.
	t.Run("all fields nil", func(t *testing.T) {
		r := SleepRow{Date: "2026-05-15"}
		got := ComputeSleepEfficiency(r)
		if got.Eligible || got.EligibilityReason != SleepEligibilityDataMissing {
			t.Fatalf("expected sleep_data_missing for fully-nil row, got %+v", got)
		}
		if got.Efficiency != nil {
			t.Error("expected nil efficiency when ineligible")
		}
	})
	// Defensive variant: total nil but staged fields somehow present.
	// Real-world this shouldn't happen (sleep_total drives the ingest),
	// but if it does we still classify as data_missing because efficiency
	// cannot be computed without a total.
	t.Run("nil total with staged fields", func(t *testing.T) {
		r := SleepRow{
			Date: "2026-05-15",
			Deep: fp(1.5), REM: fp(1.5), Core: fp(4.0),
		}
		got := ComputeSleepEfficiency(r)
		if got.Eligible || got.EligibilityReason != SleepEligibilityDataMissing {
			t.Fatalf("expected sleep_data_missing for nil-Total even with stages, got %+v", got)
		}
	})
}

func TestComputeSleepEfficiency_CoarseOnlySource(t *testing.T) {
	// Source emits sleep_total + sleep_unspecified only, no staging.
	// RingConn / iPhone Sleep Schedule case.
	r := SleepRow{
		Date:  "2026-05-15",
		Total: fp(7.2),
		Deep:  nil, REM: nil, Core: nil,
		Unspecified: fp(7.2),
		Awake:       nil,
	}
	got := ComputeSleepEfficiency(r)
	if got.Eligible || got.EligibilityReason != SleepEligibilityCoarseOnlySource {
		t.Fatalf("expected coarse_only_source, got %+v", got)
	}
}

func TestComputeSleepEfficiency_AwakeZeroExplicit(t *testing.T) {
	// Defensive: awake row present but equals 0 should behave like
	// structural zero (the qty > 0 filter normally strips this, but if
	// it ever survives we should not divide-by-something-weird).
	r := SleepRow{
		Date:  "2026-05-15",
		Total: fp(7.0),
		Deep:  fp(1.4), REM: fp(1.6), Core: fp(4.0),
		Awake: fp(0),
	}
	got := ComputeSleepEfficiency(r)
	if !got.Eligible || got.EligibilityReason != SleepEligibilityOKAwakeStructuralZero {
		t.Fatalf("expected structural-zero on awake=0 explicit, got %+v", got)
	}
	if got.Efficiency == nil || *got.Efficiency != 1.0 {
		t.Errorf("expected efficiency 1.0, got %+v", got.Efficiency)
	}
}

func TestComputeSleepEfficiency_StagedExceedsTotalSlightly(t *testing.T) {
	// Apple Watch sometimes reports staged sum slightly above sleep_total
	// (≤ 2%). Still promote to structural zero.
	r := SleepRow{
		Date:  "2026-05-15",
		Total: fp(7.0),
		Deep:  fp(1.45), REM: fp(1.65), Core: fp(4.0), // sum = 7.10, +1.4%
		Awake: nil,
	}
	got := ComputeSleepEfficiency(r)
	if !got.Eligible || got.EligibilityReason != SleepEligibilityOKAwakeStructuralZero {
		t.Fatalf("expected structural-zero on +1.4%% gap, got %+v", got)
	}
}

func TestComputeSleepEfficiency_StagedExceedsToleranceBoundary(t *testing.T) {
	// Exactly at the 2% boundary — accept.
	totalH := 7.0
	stagedSum := totalH * 1.02 // gap exactly 2.0%
	deep := stagedSum / 3
	r := SleepRow{
		Date:  "2026-05-15",
		Total: fp(totalH),
		Deep:  fp(deep), REM: fp(deep), Core: fp(deep),
		Awake: nil,
	}
	got := ComputeSleepEfficiency(r)
	if !got.Eligible || got.EligibilityReason != SleepEligibilityOKAwakeStructuralZero {
		t.Fatalf("expected structural-zero at 2%% boundary, got %+v", got)
	}
}

func TestComputeSleepEfficiency_StagedExceedsToleranceFails(t *testing.T) {
	// Just beyond the 2% boundary — reject as missing_awake_unknown.
	totalH := 7.0
	stagedSum := totalH * 1.03 // gap 3.0%
	deep := stagedSum / 3
	r := SleepRow{
		Date:  "2026-05-15",
		Total: fp(totalH),
		Deep:  fp(deep), REM: fp(deep), Core: fp(deep),
		Awake: nil,
	}
	got := ComputeSleepEfficiency(r)
	if got.Eligible || got.EligibilityReason != SleepEligibilityMissingAwakeUnknown {
		t.Fatalf("expected missing_awake_unknown beyond tolerance, got %+v", got)
	}
}

func TestComputeSleepCaptureConfidence_GoodCapture(t *testing.T) {
	got := ComputeSleepCaptureConfidence(SleepRow{
		Date:  "2026-05-15",
		Total: fp(7.5),
		Deep:  fp(1.5), REM: fp(1.8), Core: fp(4.2),
		Awake: fp(0.5),
	})
	if got.Class != SleepCaptureGood || got.LowConfidence {
		t.Fatalf("capture = %+v, want good high-confidence capture", got)
	}
	if got.Confidence < 0.9 {
		t.Fatalf("confidence = %v, want high confidence", got.Confidence)
	}
}

func TestComputeSleepCaptureConfidence_PartialShortCapture(t *testing.T) {
	got := ComputeSleepCaptureConfidence(SleepRow{
		Date:  "2026-05-15",
		Total: fp(3.99),
		Deep:  fp(1.0), REM: fp(1.0), Core: fp(1.99),
		Awake: fp(0.1),
	})
	if got.Class != SleepCapturePartialShort || !got.LowConfidence {
		t.Fatalf("capture = %+v, want partial short low-confidence capture", got)
	}
	if got.Reason != SleepCaptureReasonShortWithStages {
		t.Fatalf("reason = %q, want %q", got.Reason, SleepCaptureReasonShortWithStages)
	}
}

func TestComputeSleepCaptureConfidence_MissingCapture(t *testing.T) {
	got := ComputeSleepCaptureConfidence(SleepRow{Date: "2026-05-15"})
	if got.Class != SleepCaptureMissing || !got.LowConfidence || got.Confidence != 0 {
		t.Fatalf("capture = %+v, want missing zero-confidence capture", got)
	}
}

func TestComputeSleepCaptureConfidence_CoarseOnlyCapture(t *testing.T) {
	got := ComputeSleepCaptureConfidence(SleepRow{
		Date:        "2026-05-15",
		Total:       fp(7.2),
		Unspecified: fp(7.2),
	})
	if got.Class != SleepCaptureCoarseOnly || !got.LowConfidence {
		t.Fatalf("capture = %+v, want coarse-only low-confidence capture", got)
	}
}

func TestComputeSleepCaptureConfidence_StageMismatch(t *testing.T) {
	got := ComputeSleepCaptureConfidence(SleepRow{
		Date:  "2026-05-15",
		Total: fp(8.0),
		Deep:  fp(1.0), REM: fp(1.0), Core: fp(1.0),
	})
	if got.Class != SleepCaptureStageMismatch || !got.LowConfidence {
		t.Fatalf("capture = %+v, want stage-mismatch low-confidence capture", got)
	}
}
