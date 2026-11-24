package storage

import (
	"math"
	"testing"
)

// Gaps must interrupt a sustained run. A covered hour with a degenerate
// baseline has a known zero shift and retains its raw bpm-hour contribution.
func TestPrimaryHourlyStressSQL(t *testing.T) {
	db, done := testDB(t)
	defer done()
	for _, tc := range []struct {
		sd   float64
		want []float64
	}{
		{5, []float64{2, math.NaN(), -1, 0}},
		{0, []float64{0, math.NaN(), 0, 0}},
	} {
		var got []float64
		var overshoot float64
		err := db.pool.QueryRow(t.Context(), hourlyStressSeriesSQL, []float64{70, 90, 55, math.NaN()},
			[]bool{true, false, true, true}, 60, tc.sd).Scan(&got, &overshoot)
		if err != nil || len(got) != len(tc.want) || overshoot != 10 {
			t.Fatalf("z=%v overshoot=%v err=%v", got, overshoot, err)
		}
		for i, want := range tc.want {
			if got[i] != want && !(math.IsNaN(got[i]) && math.IsNaN(want)) {
				t.Fatalf("hour %d: got %v want %v", i, got[i], want)
			}
		}
	}
}
