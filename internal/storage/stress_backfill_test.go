package storage

import (
	"math"
	"sort"
	"testing"
)

// TestStressDistributionStats_Percentiles pins the in-memory math
// the distribution path uses against the same percentileSorted
// helper the PersonalBaseline kernel uses. Doesn't touch the DB —
// guards against regressions where the CLI's printed mean/median/p90
// diverge from what the API would return.
func TestStressDistributionStats_Percentiles(t *testing.T) {
	loads := []float64{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	sort.Float64s(loads)

	var sum float64
	for _, v := range loads {
		sum += v
	}
	mean := sum / float64(len(loads))
	median := percentileSorted(loads, 0.5)
	p90 := percentileSorted(loads, 0.9)
	max := loads[len(loads)-1]

	if math.Abs(mean-5.0) > 1e-9 {
		t.Errorf("mean = %v, want 5.0", mean)
	}
	if math.Abs(median-5.0) > 1e-9 {
		t.Errorf("median = %v, want 5.0", median)
	}
	if math.Abs(p90-9.0) > 1e-9 {
		t.Errorf("p90 = %v, want 9.0 (linear interp on [0..10])", p90)
	}
	if math.Abs(max-10.0) > 1e-9 {
		t.Errorf("max = %v, want 10.0", max)
	}
}

// (The empty-range path through ComputeStressDistributionStats
// requires a *DB and is exercised by integration. Removing the
// previous unit test that just asserted struct-literal zero values —
// it didn't validate the actual code path per CodeRabbit feedback.)
