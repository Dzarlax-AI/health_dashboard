package health

import "testing"

func TestNormalizeDistanceKm(t *testing.T) {
	tests := []struct {
		name  string
		qty   float64
		units string
		want  float64
	}{
		{name: "kilometres", qty: 10, units: "km", want: 10},
		{name: "metres", qty: 1500, units: "m", want: 1.5},
		{name: "miles", qty: 1, units: "mi", want: 1.609344},
		{name: "feet", qty: 3280.839895, units: "ft", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeDistanceKm(tt.qty, tt.units); abs(got-tt.want) > 0.000001 {
				t.Fatalf("NormalizeDistanceKm(%v, %q) = %v, want %v", tt.qty, tt.units, got, tt.want)
			}
		})
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
