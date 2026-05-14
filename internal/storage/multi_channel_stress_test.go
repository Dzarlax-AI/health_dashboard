package storage

import (
	"math"
	"testing"
)

func TestMeanFiniteHourZ(t *testing.T) {
	tests := []struct {
		name    string
		input   []float64
		wantOK  bool
		wantVal float64
	}{
		{
			name:   "empty slice",
			input:  []float64{},
			wantOK: false,
		},
		{
			name:   "all NaN",
			input:  []float64{math.NaN(), math.NaN(), math.NaN()},
			wantOK: false,
		},
		{
			name:    "mixed finite + NaN",
			input:   []float64{1.0, math.NaN(), 2.0, math.NaN(), 3.0},
			wantOK:  true,
			wantVal: 2.0,
		},
		{
			name:    "with +Inf skipped",
			input:   []float64{1.0, math.Inf(+1), 2.0},
			wantOK:  true,
			wantVal: 1.5,
		},
		{
			name:    "negative values",
			input:   []float64{-1.0, -2.0, -3.0},
			wantOK:  true,
			wantVal: -2.0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := meanFiniteHourZ(tt.input)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && math.Abs(got-tt.wantVal) > 1e-9 {
				t.Fatalf("val = %v, want %v", got, tt.wantVal)
			}
		})
	}
}
