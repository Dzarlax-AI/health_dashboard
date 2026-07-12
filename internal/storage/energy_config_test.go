package storage

import "testing"

func TestDefaultEnergyConfig(t *testing.T) {
	// Pin every default — these constants drive the bank math and
	// changing any silently shifts behaviour across all history. The
	// v2.2 fields specifically: Beta=0.8 / ZThreshold=0.5 are the
	// §6 Q3 non-production placeholders; StressDrainEnabled MUST
	// default to false so brand-new deploys don't apply β before the
	// §4.5 validation rubric clears each tenant.
	d := DefaultEnergyConfig()
	if d.Alpha != 0.08 {
		t.Errorf("Alpha = %v, want 0.08", d.Alpha)
	}
	if d.Beta != 0.8 {
		t.Errorf("Beta = %v, want 0.8 (§6 Q3 placeholder)", d.Beta)
	}
	if d.ZThreshold != 0.5 {
		t.Errorf("ZThreshold = %v, want 0.5 (§4.4 default)", d.ZThreshold)
	}
	if d.StressDrainEnabled != false {
		t.Errorf("StressDrainEnabled = %v, want false (must stay off until §4.5 validation)", d.StressDrainEnabled)
	}
	if d.AlphaFactor != 1.0 {
		t.Errorf("AlphaFactor = %v, want 1.0", d.AlphaFactor)
	}
	if d.AlphaFactorSource != "default" {
		t.Errorf("AlphaFactorSource = %q, want default", d.AlphaFactorSource)
	}
	if d.FormulaVersion != 3 {
		t.Errorf("FormulaVersion = %v, want 3 (causal missing-day imputation)", d.FormulaVersion)
	}
}

func TestEffectiveAlpha(t *testing.T) {
	c := EnergyConfig{Alpha: 0.08, AlphaFactor: 1.25}
	got := c.EffectiveAlpha()
	want := 0.08 * 1.25
	if got != want {
		t.Errorf("EffectiveAlpha = %v, want %v", got, want)
	}
}

func TestEffectiveBeta_DisabledIgnoresBeta(t *testing.T) {
	// Master gate semantics: even with Beta=0.8 (or any other value),
	// StressDrainEnabled=false MUST return 0. This is the
	// §6 Q3 production contract — the audit-trail components row
	// gets sustained_hr_load_z while the bank stays unaffected.
	for _, beta := range []float64{0, 0.1, 0.8, 5.0, -1.0} {
		c := EnergyConfig{Beta: beta, StressDrainEnabled: false}
		if got := c.EffectiveBeta(); got != 0 {
			t.Errorf("EffectiveBeta with beta=%v disabled = %v, want 0", beta, got)
		}
	}
}

func TestEffectiveBeta_EnabledReturnsBeta(t *testing.T) {
	for _, beta := range []float64{0.0, 0.1, 0.8, 5.0} {
		c := EnergyConfig{Beta: beta, StressDrainEnabled: true}
		if got := c.EffectiveBeta(); got != beta {
			t.Errorf("EffectiveBeta with beta=%v enabled = %v, want %v", beta, got, beta)
		}
	}
}

func TestEffectiveBeta_BothFieldsTogether(t *testing.T) {
	// Realistic combinations — proves the gate logic catches every
	// disabled case without exception, and lets every enabled case
	// pass Beta through unchanged.
	cases := []struct {
		beta    float64
		enabled bool
		want    float64
	}{
		{0.8, false, 0},   // default config — no drain
		{0.8, true, 0.8},  // post-validation steady state
		{0.0, true, 0.0},  // enabled but coefficient zeroed
		{0.0, false, 0.0}, // belt-and-suspenders disabled
	}
	for _, c := range cases {
		got := EnergyConfig{Beta: c.beta, StressDrainEnabled: c.enabled}.EffectiveBeta()
		if got != c.want {
			t.Errorf("Beta=%v Enabled=%v → %v, want %v", c.beta, c.enabled, got, c.want)
		}
	}
}
