package health

import (
	"math"
	"testing"
)

func TestReadinessScore_Invariants(t *testing.T) {
	cases := []struct {
		name  string
		hrv   []float64
		rhr   []float64
		sleep []float64
	}{
		{"no data neutral", nil, nil, nil},
		{"stable baseline", repeatFloat(40, minBaseline+2), repeatFloat(55, minBaseline+2), repeatFloat(7.5, minBaseline+2)},
		{"poor today", prependFloat(20, repeatFloat(50, minBaseline+1)), prependFloat(75, repeatFloat(55, minBaseline+1)), prependFloat(4.5, repeatFloat(7.5, minBaseline+1))},
		{"excellent today", prependFloat(70, repeatFloat(45, minBaseline+1)), prependFloat(45, repeatFloat(55, minBaseline+1)), prependFloat(8.0, repeatFloat(7.2, minBaseline+1))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ComputeReadinessScore(c.hrv, c.rhr, c.sleep)
			if got < 0 || got > 100 {
				t.Fatalf("readiness score out of bounds: %d", got)
			}
		})
	}
}

func TestReadinessScore_MinimumDataGateAndZeroVariance(t *testing.T) {
	if got := ComputeReadinessScore(repeatFloat(40, minBaseline+1), repeatFloat(55, minBaseline+1), repeatFloat(7.5, minBaseline+1)); got != 70 {
		t.Fatalf("readiness with insufficient baseline = %d, want neutral 70", got)
	}

	got := ComputeReadinessScore(
		repeatFloat(40, minBaseline+2),
		repeatFloat(55, minBaseline+2),
		repeatFloat(7.5, minBaseline+2),
	)
	if got != 70 {
		t.Fatalf("readiness with zero-variance baseline = %d, want neutral 70", got)
	}
}

func TestReadinessScore_DirectionalMonotonicity(t *testing.T) {
	baselineHRV := alternatingFloat(39, 41, 14)
	baselineRHR := alternatingFloat(54, 56, 14)
	baselineSleep := alternatingFloat(7.1, 7.3, 14)

	strong := ComputeReadinessScore(
		prependFloat(52, append(repeatFloat(48, 6), baselineHRV...)),
		prependFloat(48, append(repeatFloat(50, 6), baselineRHR...)),
		prependFloat(8.0, append(repeatFloat(7.8, 6), baselineSleep...)),
	)
	weak := ComputeReadinessScore(
		prependFloat(28, append(repeatFloat(32, 6), baselineHRV...)),
		prependFloat(70, append(repeatFloat(66, 6), baselineRHR...)),
		prependFloat(5.2, append(repeatFloat(5.6, 6), baselineSleep...)),
	)
	if strong <= weak {
		t.Fatalf("strong readiness = %d, weak readiness = %d, want strong > weak", strong, weak)
	}
}

func TestReadinessBand_MonotonicBoundaries(t *testing.T) {
	cases := []struct {
		score int
		want  string
	}{
		{0, "low"},
		{49, "low"},
		{50, "fair"},
		{79, "fair"},
		{80, "optimal"},
		{100, "optimal"},
	}
	for _, c := range cases {
		if got := ReadinessBand(c.score); got != c.want {
			t.Fatalf("ReadinessBand(%d) = %q, want %q", c.score, got, c.want)
		}
	}
}

func TestScoreSleep_StatusFloorPreventsShortSleepGood(t *testing.T) {
	sri := 82.0
	sec := scoreSleep(RawMetrics{
		Sleep:                 []float64{5.8, 5.9, 5.7, 7.4, 7.5, 7.4, 7.5},
		Deep:                  []float64{1.5, 1.4, 1.4},
		Awake:                 []float64{0.1, 0.1, 0.1},
		SleepRegularityIndex:  &sri,
		SleepRegularityNights: 14,
	}, GetStrings("en"))
	if sec == nil {
		t.Fatalf("scoreSleep returned nil")
	}
	if sec.Status == "good" {
		t.Fatalf("short sleep status = %q, want not good", sec.Status)
	}
	if sec.Status != "fair" {
		t.Fatalf("short sleep status = %q, want fair", sec.Status)
	}
}

func TestComputeSleepAnalysis_IgnoresZeroNightsAndClampsEfficiency(t *testing.T) {
	got := computeSleepAnalysis(RawMetrics{
		Sleep: []float64{5, 0, 7},
		Deep:  []float64{1, 0, 1.4},
		REM:   []float64{1, 0, 1.5},
		Awake: []float64{6, 0, 0.2},
	})
	if got == nil {
		t.Fatalf("computeSleepAnalysis returned nil")
	}
	if got.Nights != 2 {
		t.Fatalf("Nights = %d, want 2", got.Nights)
	}
	if got.TotalAvg != 6 {
		t.Fatalf("TotalAvg = %v, want 6", got.TotalAvg)
	}
	if got.Efficiency != 0 {
		t.Fatalf("Efficiency = %v, want clamp to 0", got.Efficiency)
	}
}

func TestComputeAlerts_RequiresBaselineVariance(t *testing.T) {
	alerts := computeAlerts(RawMetrics{
		Resp:      repeatFloat(18, minBaseline+2),
		WristTemp: repeatFloat(36.8, minBaseline+2),
		HRV:       repeatFloat(40, minBaseline+2),
	}, GetStrings("en"))
	if len(alerts) != 0 {
		t.Fatalf("alerts with zero-variance baselines = %+v, want none", alerts)
	}
}

func TestComputeEnergyBank_MissingInputsReturnNil(t *testing.T) {
	ls := GetStrings("en")
	if got := computeEnergyBank(RawMetrics{}, 70, nil, ls); got != nil {
		t.Fatalf("computeEnergyBank without baseline data = %+v, want nil", got)
	}
	d := RawMetrics{
		HRV:   repeatFloat(40, minBaseline+2),
		Sleep: append([]float64{0}, repeatFloat(7.5, minBaseline+1)...),
	}
	if got := computeEnergyBank(d, 70, nil, ls); got != nil {
		t.Fatalf("computeEnergyBank with zero sleep today = %+v, want nil", got)
	}
}

func TestComputeStrain_ClampsAndHandlesMissingBaselines(t *testing.T) {
	if got, _ := computeStrain(RawMetrics{}); got != 0 {
		t.Fatalf("computeStrain without baselines = %d, want 0", got)
	}
	got, _ := computeStrain(RawMetrics{
		StepsToday:             50000,
		StepsChronic28d:        5000,
		ActiveEnergyToday:      5000,
		ActiveEnergyChronic28d: 500,
	})
	if got != 100 {
		t.Fatalf("computeStrain high ratios = %d, want clamp to 100", got)
	}
}

func TestComputeAutonomicStress_Invariants(t *testing.T) {
	neutral := RawMetrics{
		HRV: repeatFloat(40, minBaseline+2),
		RHR: repeatFloat(55, minBaseline+2),
	}
	score, _, hrvZ := computeAutonomicStress(neutral)
	if score < 0 || score > 100 || math.IsNaN(hrvZ) || math.IsInf(hrvZ, 0) {
		t.Fatalf("neutral autonomic stress score=%d hrvZ=%v, want finite score in [0,100]", score, hrvZ)
	}

	loaded := RawMetrics{
		HRV: prependFloat(20, alternatingFloat(48, 52, minBaseline+1)),
		RHR: prependFloat(75, alternatingFloat(53, 57, minBaseline+1)),
	}
	score, _, hrvZ = computeAutonomicStress(loaded)
	if score <= 0 || score > 100 {
		t.Fatalf("loaded autonomic stress score=%d, want positive score in [1,100]", score)
	}
	if hrvZ >= 0 {
		t.Fatalf("loaded HRV z = %v, want negative", hrvZ)
	}
}

func TestChooseVerdictV2_PrecedenceBoundaries(t *testing.T) {
	bands := VerdictBands{Rest: 15, Recovery: 41, PushHard: 55}
	cases := []struct {
		name string
		hrvZ float64
		bank int
		want string
	}{
		{"hrv rest overrides high bank", hrvZRestBand, 100, "rest"},
		{"bank rest boundary", 0, bands.Rest, "rest"},
		{"hrv recovery boundary", hrvZLowerBand, 100, "active_recovery"},
		{"bank recovery boundary", 0, bands.Recovery, "active_recovery"},
		{"push requires both boundaries", hrvZUpperBand, bands.PushHard, "push_hard"},
		{"below push bank stays moderate", hrvZUpperBand, bands.PushHard - 1, "moderate"},
		{"below push hrv stays moderate", hrvZUpperBand - 0.01, bands.PushHard, "moderate"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ChooseVerdictV2(c.hrvZ, c.bank, bands); got != c.want {
				t.Fatalf("ChooseVerdictV2(%v, %d) = %q, want %q", c.hrvZ, c.bank, got, c.want)
			}
		})
	}
}

func TestComputeBriefing_StressHeadlineVetoesPushHard(t *testing.T) {
	d := RawMetrics{
		LastDate:               "2026-06-01",
		HRV:                    prependFloat(80, alternatingFloat(38, 42, minBaseline+1)),
		RHR:                    prependFloat(65, alternatingFloat(53, 57, minBaseline+1)),
		Resp:                   prependFloat(20, repeatFloat(14, minBaseline+1)),
		Sleep:                  prependFloat(6.0, alternatingFloat(7.4, 7.6, minBaseline+1)),
		StepsToday:             0,
		StepsChronic28d:        10000,
		ActiveEnergyToday:      0,
		ActiveEnergyChronic28d: 500,
	}
	resp := ComputeBriefing(d, "en")
	if resp.EnergyBank == nil {
		t.Fatalf("EnergyBank missing")
	}
	if resp.Headline == nil || resp.Headline.Key != "stress" {
		t.Fatalf("Headline = %+v, want stress", resp.Headline)
	}
	if resp.EnergyBank.ActionVerdict == "push_hard" {
		t.Fatalf("stress headline must veto push_hard, got %+v", resp.EnergyBank)
	}
}

func repeatFloat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

func prependFloat(v float64, rest []float64) []float64 {
	out := make([]float64, 0, len(rest)+1)
	out = append(out, v)
	out = append(out, rest...)
	return out
}

func alternatingFloat(a, b float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = a
		} else {
			out[i] = b
		}
	}
	return out
}
