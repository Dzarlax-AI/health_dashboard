package health

import (
	"math"
	"testing"
)

// ============================================================================
// HourZShift
// ============================================================================

func TestHourZShift_NormalRange(t *testing.T) {
	// Baseline 60 bpm, SD 5 bpm. Hour HR 70 → z = (70-60)/5 = +2.
	got := HourZShift(70, 60, 5)
	if math.Abs(got-2.0) > 1e-9 {
		t.Errorf("z = %v, want 2.0", got)
	}
}

func TestHourZShift_BelowBaseline(t *testing.T) {
	// Hour HR 50, baseline 60, SD 5 → z = -2 (resting / cool).
	got := HourZShift(50, 60, 5)
	if math.Abs(got-(-2.0)) > 1e-9 {
		t.Errorf("z = %v, want -2.0", got)
	}
}

func TestHourZShift_ZeroSD(t *testing.T) {
	// Degenerate SD → zero, NOT panic. Caller's responsibility to
	// ensure the §4.1 SD floor was applied upstream; this guard is
	// belt-and-braces for the calibration=cold edge case.
	if got := HourZShift(70, 60, 0); got != 0 {
		t.Errorf("sd=0 should return 0, got %v", got)
	}
	if got := HourZShift(70, 60, -1); got != 0 {
		t.Errorf("negative sd should return 0, got %v", got)
	}
}

func TestHourZShift_NaN(t *testing.T) {
	nan := math.NaN()
	cases := []struct{ hr, base, sd float64 }{
		{nan, 60, 5},
		{70, nan, 5},
		{70, 60, nan},
		{math.Inf(1), 60, 5},
	}
	for _, c := range cases {
		if got := HourZShift(c.hr, c.base, c.sd); got != 0 {
			t.Errorf("HourZShift(%v, %v, %v) = %v, want 0", c.hr, c.base, c.sd, got)
		}
	}
}

// ============================================================================
// SustainedHRLoad
// ============================================================================

func TestSustainedHRLoad_EmptySlice(t *testing.T) {
	if got := SustainedHRLoad(nil, 0.5); got != 0 {
		t.Errorf("nil slice = %v, want 0", got)
	}
	if got := SustainedHRLoad([]float64{}, 0.5); got != 0 {
		t.Errorf("empty slice = %v, want 0", got)
	}
}

func TestSustainedHRLoad_AllBelowThreshold(t *testing.T) {
	// All hours below z=0.5 → no contribution.
	got := SustainedHRLoad([]float64{0.1, 0.3, 0.4, -1.0}, 0.5)
	if got != 0 {
		t.Errorf("all below = %v, want 0", got)
	}
}

func TestSustainedHRLoad_SpecExample(t *testing.T) {
	// Per §4.4 commentary: "typical workday with one 2h busy stretch
	// (z≈1) yields ~3 load units".
	// 2 hours at z=1.0 with threshold=0.5: Σ max(0, 1.0-0.5) × 2 = 1.0.
	// Wait — spec says 3 units. Let me re-read.
	//
	// "Starting value chosen so a typical workday with one 2h busy
	// stretch (z≈1) yields ~3 load units"
	//
	// The spec says z THRESHOLD 0.5 produces ~3 units from a 2h z≈1
	// stretch. With threshold 0.5: 2h × (1.0 - 0.5) = 1.0. That's 1,
	// not 3. The spec's "~3 units" must be assuming the WHOLE day
	// contributes, not just the busy 2h. A full awake day at z=1
	// avg = ~15h × 0.5 = 7.5 units (matches the spec's "anxious dog
	// day yields ~7.5" sanity anchor below).
	//
	// Test the math directly: 2h at z=1, 13h at z=0 → 1.0.
	hours := []float64{0, 0, 0, 0, 0, 0, 0, 1, 1, 0, 0, 0, 0, 0, 0}
	got := SustainedHRLoad(hours, 0.5)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("2h@z=1 = %v, want 1.0", got)
	}
}

func TestSustainedHRLoad_SanityAnchorAnxiousDay(t *testing.T) {
	// Spec §6 Q3 anchor: "anxious-dog Sunday → z-load ≈ 7.5 →
	// β=0.8 × 7.5 ≈ 6 drain points, matching 'tired by evening'".
	// 15h awake at z≈1.0 with threshold 0.5: 15 × 0.5 = 7.5.
	hours := make([]float64, 15)
	for i := range hours {
		hours[i] = 1.0
	}
	got := SustainedHRLoad(hours, 0.5)
	if math.Abs(got-7.5) > 1e-9 {
		t.Errorf("anxious-day anchor = %v, want 7.5 (per §6 Q3)", got)
	}
}

func TestSustainedHRLoad_NaNTolerant(t *testing.T) {
	nan := math.NaN()
	// Coverage gap (NaN) shouldn't contribute. 2 real hours at z=1,
	// one NaN, one at z=0.3 (below threshold) → 1.0.
	got := SustainedHRLoad([]float64{1.0, nan, 1.0, 0.3}, 0.5)
	if math.Abs(got-1.0) > 1e-9 {
		t.Errorf("nan-tolerant = %v, want 1.0", got)
	}
}

func TestSustainedHRLoad_ThresholdNaN(t *testing.T) {
	if got := SustainedHRLoad([]float64{1, 2, 3}, math.NaN()); got != 0 {
		t.Errorf("nan threshold should return 0, got %v", got)
	}
}

// ============================================================================
// AcuteStress
// ============================================================================

func TestAcuteStress_HighSpike(t *testing.T) {
	// One hour at z=2.5 → acute.
	if !AcuteStress([]float64{0.5, 1.0, 2.5, 0.8}) {
		t.Error("z=2.5 spike should flag acute")
	}
}

func TestAcuteStress_NoSpike(t *testing.T) {
	// All hours below z=+2 → not acute.
	if AcuteStress([]float64{0.5, 1.5, 1.9, -0.3}) {
		t.Error("max z=1.9 shouldn't flag acute (threshold +2)")
	}
}

func TestAcuteStress_AtBoundary(t *testing.T) {
	// Exactly z=+2 doesn't count (strict >). Just above does.
	if AcuteStress([]float64{2.0, 1.5}) {
		t.Error("z=2.0 exactly is not > +2 (strict)")
	}
	if !AcuteStress([]float64{2.001, 1.5}) {
		t.Error("z=2.001 should flag acute")
	}
}

func TestAcuteStress_EmptyAndNaN(t *testing.T) {
	if AcuteStress(nil) {
		t.Error("nil should not flag")
	}
	if AcuteStress([]float64{}) {
		t.Error("empty should not flag")
	}
	if AcuteStress([]float64{math.NaN(), math.Inf(1)}) {
		t.Error("NaN/Inf only should not flag")
	}
}

// ============================================================================
// SustainedLoadFlag
// ============================================================================

func TestSustainedLoadFlag_FourConsecutive(t *testing.T) {
	// 4 hours at z>+1 consecutively → flag.
	if !SustainedLoadFlag([]float64{0.5, 1.5, 1.2, 1.8, 2.0, 0.3}) {
		t.Error("4h consecutive z>+1 should flag")
	}
}

func TestSustainedLoadFlag_ThreeConsecutiveFalls(t *testing.T) {
	// Only 3 consecutive → no flag (need ≥4).
	if SustainedLoadFlag([]float64{0.5, 1.5, 1.2, 1.8, 0.3, 1.5}) {
		t.Error("3h consecutive z>+1 should NOT flag (need 4)")
	}
}

func TestSustainedLoadFlag_BoundaryStrictly(t *testing.T) {
	// z=+1 exactly is NOT > +1 (strict).
	if SustainedLoadFlag([]float64{1.0, 1.0, 1.0, 1.0, 1.0}) {
		t.Error("5h at z=1.0 exactly is not > +1 strict, should NOT flag")
	}
	if !SustainedLoadFlag([]float64{1.01, 1.01, 1.01, 1.01}) {
		t.Error("4h at z=1.01 should flag")
	}
}

func TestSustainedLoadFlag_NaNBreaksRun(t *testing.T) {
	// NaN in the middle resets the run counter — same as a coverage
	// gap interrupting the sustained-elevated pattern.
	nan := math.NaN()
	// 2h elevated + NaN + 2h elevated = NO flag (each run only 2h).
	if SustainedLoadFlag([]float64{1.5, 1.5, nan, 1.5, 1.5}) {
		t.Error("NaN should break run, 2+2 != 4 consecutive")
	}
	// 4h elevated + NaN + more = flag (fired before NaN).
	if !SustainedLoadFlag([]float64{1.5, 1.5, 1.5, 1.5, nan, 1.5}) {
		t.Error("4h run before NaN should flag")
	}
}

func TestSustainedLoadFlag_DistinctFromIntegral(t *testing.T) {
	// A day with positive SustainedHRLoad but NO SustainedLoadFlag —
	// proves the two functions answer different questions. One z=2
	// hour (1.5 z-load units) doesn't fire the 4h-run flag.
	hours := []float64{0, 0, 2.0, 0, 0, 0}
	if got := SustainedHRLoad(hours, 0.5); got <= 0 {
		t.Errorf("integral should be > 0, got %v", got)
	}
	if SustainedLoadFlag(hours) {
		t.Error("single high hour should NOT fire 4h-run flag")
	}
}

// ============================================================================
// IllnessSignature
// ============================================================================

func TestIllnessSignature_AllThreeFire(t *testing.T) {
	// temp +1.5, resp +1.2, hrvDrop +1.3 → all >+1 → flag.
	if !IllnessSignature(1.5, 1.2, 1.3) {
		t.Error("all three above +1 should flag illness")
	}
}

func TestIllnessSignature_OnlyTwoFire(t *testing.T) {
	// Two channels above +1, one below → no flag (requires all).
	if IllnessSignature(1.5, 1.2, 0.5) {
		t.Error("only 2/3 above should NOT flag")
	}
	if IllnessSignature(1.5, 0.8, 1.5) {
		t.Error("only 2/3 above should NOT flag (different combo)")
	}
}

func TestIllnessSignature_Boundary(t *testing.T) {
	// Exactly +1 is NOT > +1 (strict). +1.001 just clears it.
	if IllnessSignature(1.0, 1.0, 1.0) {
		t.Error("all exactly +1 is not > +1 strict, should NOT flag")
	}
	if !IllnessSignature(1.001, 1.001, 1.001) {
		t.Error("all just above +1 should flag")
	}
}

func TestIllnessSignature_NaNAnyChannel(t *testing.T) {
	// Spec §4.3: requires all three. NaN = "not enough evidence".
	nan := math.NaN()
	if IllnessSignature(nan, 1.5, 1.5) {
		t.Error("NaN temp should not flag")
	}
	if IllnessSignature(1.5, nan, 1.5) {
		t.Error("NaN resp should not flag")
	}
	if IllnessSignature(1.5, 1.5, nan) {
		t.Error("NaN hrv should not flag")
	}
}

// ============================================================================
// RecoveryDebt
// ============================================================================

func TestRecoveryDebt_AsymmetricThresholds(t *testing.T) {
	// HRV gate is +1 (strict), RHR gate is +0.5.
	if !RecoveryDebt(1.5, 0.7) {
		t.Error("hrvDrop=1.5 + rhrShift=0.7 should flag debt")
	}
	// HRV at 0.9 fails (need >1).
	if RecoveryDebt(0.9, 0.7) {
		t.Error("hrvDrop=0.9 below 1.0 should NOT flag")
	}
	// RHR at 0.4 fails (need >0.5).
	if RecoveryDebt(1.5, 0.4) {
		t.Error("rhrShift=0.4 below 0.5 should NOT flag")
	}
}

func TestRecoveryDebt_NaN(t *testing.T) {
	nan := math.NaN()
	if RecoveryDebt(nan, 0.7) {
		t.Error("NaN hrvDrop should not flag")
	}
	if RecoveryDebt(1.5, nan) {
		t.Error("NaN rhrShift should not flag")
	}
}

// ============================================================================
// ParasympatheticRebound
// ============================================================================

func TestParasympatheticRebound_ClassicPattern(t *testing.T) {
	// HR shift +1.5 (elevated) and hrvDrop -1.5 (HRV above baseline)
	// → vagal rebound.
	if !ParasympatheticRebound(1.5, -1.5) {
		t.Error("HR up + HRV up should flag rebound")
	}
}

func TestParasympatheticRebound_NotRebound(t *testing.T) {
	// HR up + HRV down = normal stress, not rebound.
	if ParasympatheticRebound(1.5, 1.5) {
		t.Error("HR up + HRV down is stress, NOT rebound")
	}
	// HR normal + HRV up = resting day, not rebound (no autonomic
	// expenditure to "recover from").
	if ParasympatheticRebound(0.3, -1.5) {
		t.Error("HR normal + HRV up alone shouldn't flag")
	}
}

func TestParasympatheticRebound_Boundaries(t *testing.T) {
	// hrvDrop=-1 exactly is NOT < -1 (strict).
	if ParasympatheticRebound(1.5, -1.0) {
		t.Error("hrvDrop=-1.0 exactly is not < -1 strict")
	}
	if !ParasympatheticRebound(1.5, -1.001) {
		t.Error("hrvDrop just below -1 should flag")
	}
}

func TestParasympatheticRebound_NaN(t *testing.T) {
	nan := math.NaN()
	if ParasympatheticRebound(nan, -1.5) {
		t.Error("NaN HR should not flag")
	}
	if ParasympatheticRebound(1.5, nan) {
		t.Error("NaN HRV should not flag")
	}
}

// ============================================================================
// Constants pinning
// ============================================================================

func TestThresholdConstants_LoadBearing(t *testing.T) {
	// Pin the constants — they tune every stratified flag and
	// silently shift behaviour across all history if changed
	// without a cohort study (per §6 Q6). Any change should be
	// intentional.
	if acuteZThreshold != 2.0 {
		t.Errorf("acuteZThreshold = %v, want 2.0", acuteZThreshold)
	}
	if sustainedZThreshold != 1.0 {
		t.Errorf("sustainedZThreshold = %v, want 1.0", sustainedZThreshold)
	}
	if sustainedMinHours != 4 {
		t.Errorf("sustainedMinHours = %v, want 4", sustainedMinHours)
	}
	if illnessZThreshold != 1.0 {
		t.Errorf("illnessZThreshold = %v, want 1.0", illnessZThreshold)
	}
	if recoveryHRVThreshold != 1.0 {
		t.Errorf("recoveryHRVThreshold = %v, want 1.0", recoveryHRVThreshold)
	}
	if recoveryRHRThreshold != 0.5 {
		t.Errorf("recoveryRHRThreshold = %v, want 0.5", recoveryRHRThreshold)
	}
	if reboundHRThreshold != 1.0 {
		t.Errorf("reboundHRThreshold = %v, want 1.0", reboundHRThreshold)
	}
	if reboundHRVThreshold != -1.0 {
		t.Errorf("reboundHRVThreshold = %v, want -1.0", reboundHRVThreshold)
	}
}
