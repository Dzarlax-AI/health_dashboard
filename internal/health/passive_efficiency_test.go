package health

import "testing"

func TestComputeWalkingHREligibility_NormalValue(t *testing.T) {
	r := WalkingHRRow{Date: "2026-05-15", Value: fp(101.5)}
	got := ComputeWalkingHREligibility(r)
	if !got.Eligible || got.EligibilityReason != PassiveEfficiencyOK {
		t.Fatalf("expected ok, got %+v", got)
	}
	if got.Value == nil || *got.Value != 101.5 {
		t.Errorf("expected value 101.5, got %+v", got.Value)
	}
}

func TestComputeWalkingHREligibility_NoWalkingHR(t *testing.T) {
	r := WalkingHRRow{Date: "2026-05-15", Value: nil}
	got := ComputeWalkingHREligibility(r)
	if got.Eligible || got.EligibilityReason != PassiveEfficiencyNoWalkingHR {
		t.Fatalf("expected no_walking_hr, got %+v", got)
	}
	if got.Value != nil {
		t.Error("expected nil value when ineligible")
	}
}

func TestComputeWalkingHREligibility_OutOfRangeLow(t *testing.T) {
	// 35 bpm — physiologically implausible for walking. Treat as artifact.
	r := WalkingHRRow{Date: "2026-05-15", Value: fp(35)}
	got := ComputeWalkingHREligibility(r)
	if got.Eligible || got.EligibilityReason != PassiveEfficiencyWalkingHROutOfRange {
		t.Fatalf("expected walking_hr_out_of_range (low), got %+v", got)
	}
}

func TestComputeWalkingHREligibility_OutOfRangeHigh(t *testing.T) {
	// 200 bpm — above the 180 ceiling. Reject as artifact.
	r := WalkingHRRow{Date: "2026-05-15", Value: fp(200)}
	got := ComputeWalkingHREligibility(r)
	if got.Eligible || got.EligibilityReason != PassiveEfficiencyWalkingHROutOfRange {
		t.Fatalf("expected walking_hr_out_of_range (high), got %+v", got)
	}
}

func TestComputeWalkingHREligibility_BoundaryLow(t *testing.T) {
	// Exactly at the 50 bpm floor — accept.
	r := WalkingHRRow{Date: "2026-05-15", Value: fp(50)}
	got := ComputeWalkingHREligibility(r)
	if !got.Eligible || got.EligibilityReason != PassiveEfficiencyOK {
		t.Fatalf("expected ok at floor boundary, got %+v", got)
	}
}

func TestComputeWalkingHREligibility_BoundaryHigh(t *testing.T) {
	// Exactly at the 180 bpm ceiling — accept.
	r := WalkingHRRow{Date: "2026-05-15", Value: fp(180)}
	got := ComputeWalkingHREligibility(r)
	if !got.Eligible || got.EligibilityReason != PassiveEfficiencyOK {
		t.Fatalf("expected ok at ceiling boundary, got %+v", got)
	}
}
