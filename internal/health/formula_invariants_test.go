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
