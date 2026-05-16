package health

import "testing"

// Defaults must match the values calibrated against the `health` tenant
// in PR #97. Any future tuning has to change the consts AND the test —
// the test is the canary that catches accidental const drift.
func TestDefaultChronicLoadConfig(t *testing.T) {
	got := DefaultChronicLoadConfig()
	if got.MinBreachDays != ChronicLoadMinBreachDays {
		t.Errorf("MinBreachDays = %d, want const %d", got.MinBreachDays, ChronicLoadMinBreachDays)
	}
	if got.MinAcuteDensity != ChronicLoadMinAcuteDensity {
		t.Errorf("MinAcuteDensity = %d, want const %d", got.MinAcuteDensity, ChronicLoadMinAcuteDensity)
	}
}

func TestChronicLoadConfigSanitize(t *testing.T) {
	cases := []struct {
		name              string
		in                ChronicLoadConfig
		wantCorrected     bool
		wantBreachDays    int
		wantAcuteDensity  int
	}{
		{
			name:             "in-range values pass through unchanged",
			in:               ChronicLoadConfig{MinBreachDays: 6, MinAcuteDensity: 9},
			wantCorrected:    false,
			wantBreachDays:   6,
			wantAcuteDensity: 9,
		},
		{
			name:             "zero MinBreachDays replaced with default",
			in:               ChronicLoadConfig{MinBreachDays: 0, MinAcuteDensity: 9},
			wantCorrected:    true,
			wantBreachDays:   ChronicLoadMinBreachDays,
			wantAcuteDensity: 9,
		},
		{
			name:             "negative MinAcuteDensity replaced with default",
			in:               ChronicLoadConfig{MinBreachDays: 6, MinAcuteDensity: -1},
			wantCorrected:    true,
			wantBreachDays:   6,
			wantAcuteDensity: ChronicLoadMinAcuteDensity,
		},
		{
			name:             "both invalid replaced with defaults",
			in:               ChronicLoadConfig{MinBreachDays: 0, MinAcuteDensity: 0},
			wantCorrected:    true,
			wantBreachDays:   ChronicLoadMinBreachDays,
			wantAcuteDensity: ChronicLoadMinAcuteDensity,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, corrected := tc.in.Sanitize()
			if corrected != tc.wantCorrected {
				t.Errorf("corrected = %v, want %v", corrected, tc.wantCorrected)
			}
			if out.MinBreachDays != tc.wantBreachDays {
				t.Errorf("MinBreachDays = %d, want %d", out.MinBreachDays, tc.wantBreachDays)
			}
			if out.MinAcuteDensity != tc.wantAcuteDensity {
				t.Errorf("MinAcuteDensity = %d, want %d", out.MinAcuteDensity, tc.wantAcuteDensity)
			}
		})
	}
}
