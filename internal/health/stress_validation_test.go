package health

import (
	"math"
	"testing"
)

func TestPearsonR_PerfectPositive(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5}
	ys := []float64{2, 4, 6, 8, 10}
	r, n, ok := PearsonR(xs, ys)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if n != 5 {
		t.Errorf("n = %d, want 5", n)
	}
	if math.Abs(r-1.0) > 1e-9 {
		t.Errorf("r = %v, want 1.0", r)
	}
}

func TestPearsonR_PerfectNegative(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5}
	ys := []float64{10, 8, 6, 4, 2}
	r, _, ok := PearsonR(xs, ys)
	if !ok || math.Abs(r+1.0) > 1e-9 {
		t.Errorf("r = %v, want -1.0 (ok=%v)", r, ok)
	}
}

func TestPearsonR_SkipsNaN(t *testing.T) {
	// Paired-skip: (1,2) and (3,6) survive → perfect +1.
	xs := []float64{1, math.NaN(), 3, 4}
	ys := []float64{2, 4, 6, math.NaN()}
	r, n, ok := PearsonR(xs, ys)
	if !ok || n != 2 {
		// n=2 returns ok=false per the n<3 guard
		if !ok && n == 2 {
			return // expected — too few pairs
		}
		t.Errorf("got r=%v n=%d ok=%v", r, n, ok)
	}
}

func TestPearsonR_ZeroVariance(t *testing.T) {
	xs := []float64{1, 1, 1, 1}
	ys := []float64{1, 2, 3, 4}
	_, _, ok := PearsonR(xs, ys)
	if ok {
		t.Error("expected ok=false on zero variance")
	}
}

func TestPearsonR_LengthMismatch(t *testing.T) {
	_, _, ok := PearsonR([]float64{1, 2}, []float64{1, 2, 3})
	if ok {
		t.Error("expected ok=false on length mismatch")
	}
}

func TestRubricDecide_Validated(t *testing.T) {
	r := &ValidationReport{
		Channel1HRV:   ValidationChannel{R: f(-0.45), N: 25},
		Channel2RHR:   ValidationChannel{R: f(0.30), N: 25},
		Channel3Sleep: SleepChannel{AgreementVotes: 2, N: 25},
	}
	RubricDecide(r)
	if r.Verdict != "validated" {
		t.Errorf("verdict = %q, want validated", r.Verdict)
	}
}

func TestRubricDecide_StrongHRVButBothCrossDisagree_Weak(t *testing.T) {
	r := &ValidationReport{
		Channel1HRV:   ValidationChannel{R: f(-0.45), N: 25},
		Channel2RHR:   ValidationChannel{R: f(-0.20), N: 25}, // negative → disagrees
		Channel3Sleep: SleepChannel{AgreementVotes: 0, N: 25},
	}
	RubricDecide(r)
	if r.Verdict != "weak" {
		t.Errorf("disagreement override: verdict = %q, want weak", r.Verdict)
	}
}

func TestRubricDecide_WrongDirection(t *testing.T) {
	r := &ValidationReport{
		Channel1HRV: ValidationChannel{R: f(+0.15), N: 25},
	}
	RubricDecide(r)
	if r.Verdict != "wrong_direction" {
		t.Errorf("verdict = %q, want wrong_direction", r.Verdict)
	}
}

func TestRubricDecide_Inconclusive_BelowMagnitude(t *testing.T) {
	r := &ValidationReport{
		Channel1HRV: ValidationChannel{R: f(-0.05), N: 25},
	}
	RubricDecide(r)
	if r.Verdict != "inconclusive" {
		t.Errorf("verdict = %q, want inconclusive", r.Verdict)
	}
}

func TestRubricDecide_Weak_MidBand(t *testing.T) {
	r := &ValidationReport{
		Channel1HRV:   ValidationChannel{R: f(-0.20), N: 25},
		Channel2RHR:   ValidationChannel{R: f(0.15), N: 25},
		Channel3Sleep: SleepChannel{AgreementVotes: 2, N: 25},
	}
	RubricDecide(r)
	if r.Verdict != "weak" {
		t.Errorf("verdict = %q, want weak", r.Verdict)
	}
}

func TestRubricDecide_SparseHRV_FallbackInconclusive(t *testing.T) {
	r := &ValidationReport{
		Channel1HRV: ValidationChannel{Sparse: true, N: 8},
	}
	RubricDecide(r)
	if r.Verdict != "inconclusive" {
		t.Errorf("verdict = %q, want inconclusive (sparse + no fallback data)", r.Verdict)
	}
}

func TestRubricDecide_SparseHRV_FallbackWeak(t *testing.T) {
	r := &ValidationReport{
		Channel1HRV:   ValidationChannel{Sparse: true, N: 8},
		Channel2RHR:   ValidationChannel{R: f(0.25), N: 25},
		Channel3Sleep: SleepChannel{AgreementVotes: 3, N: 25},
	}
	RubricDecide(r)
	if r.Verdict != "weak" {
		t.Errorf("verdict = %q, want weak (fallback 2-of-3 satisfied)", r.Verdict)
	}
}

func f(v float64) *float64 { return &v }
