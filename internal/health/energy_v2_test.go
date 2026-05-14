package health

import (
	"math"
	"testing"
)

const eps = 1e-9

func approxEq(t *testing.T, name string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Fatalf("%s: got %g, want %g (±%g)", name, got, want, tol)
	}
}

// TestSleepQuality_TableDriven exercises the named scenarios from
// ENERGY_BANK.md and the boundary cases that have bitten earlier
// formula iterations (zero sleep, fragmented night, apnea-proxy deep
// ratio, severe deficit).
func TestSleepQuality_TableDriven(t *testing.T) {
	cases := []struct {
		name                          string
		totalH, deepH, remH, awakeH   float64
		want                          float64
		tol                           float64
	}{
		{
			// Textbook ideal: 8h, no fragmentation, healthy stages.
			// All three factors land at 1.0, product is 1.0 → maximum
			// asymptotic refill. The ceiling is real but reachable.
			name:   "ideal_night",
			totalH: 8.0, deepH: 1.2, remH: 1.6, awakeH: 0.0,
			want: 1.0, tol: eps,
		},
		{
			// Short sleep: duration factor = 5/8 = 0.625, structure and
			// efficiency near 1. Product ≈ 0.61.
			name:   "short_5h",
			totalH: 5.0, deepH: 0.75, remH: 1.0, awakeH: 0.0,
			want: 0.625, tol: 0.01,
		},
		{
			// Total = 0: data gap. Must return 0 so the imputation path
			// (PR4) takes over instead of integrating runaway drain.
			name:   "zero_sleep_gap",
			totalH: 0, deepH: 0, remH: 0, awakeH: 0,
			want: 0, tol: eps,
		},
		{
			// Fragmented: 7h sleep + 1h awake. Efficiency = 7/8 = 0.875,
			// duration = 7/8 = 0.875. Structure full at 1.0 (deep/rem
			// percentages above thresholds). Product ≈ 0.766.
			name:   "fragmented_7h_1awake",
			totalH: 7.0, deepH: 1.1, remH: 1.5, awakeH: 1.0,
			want: 0.875 * 0.875, tol: 0.01,
		},
		{
			// Apnea proxy: deep ratio at 8% (well below 15% threshold).
			// Shortfall = 0.07; rem stays healthy. Structure factor =
			// 1.0 - 0.07 = 0.93.
			name:   "apnea_proxy_low_deep",
			totalH: 8.0, deepH: 0.64, remH: 1.6, awakeH: 0.0,
			want: 0.93, tol: 0.01,
		},
		{
			// Both stages deficient (deep=5%, rem=10%): shortfalls
			// 0.10 + 0.10 = 0.20. Structure clamped to max(0.5, 0.80)
			// = 0.80. Duration & efficiency 1.0 → product 0.80.
			name:   "both_stages_deficient",
			totalH: 8.0, deepH: 0.4, remH: 0.8, awakeH: 0.0,
			want: 0.80, tol: 0.01,
		},
		{
			// Worst case for structure within the formula's reach: zero
			// deep AND zero rem. Shortfalls = 0.15 + 0.20 = 0.35; the
			// 0.5 floor would only bite under additional penalties
			// future formulas might add. With duration & efficiency at
			// 1.0, product = 0.65.
			name:   "no_deep_no_rem",
			totalH: 8.0, deepH: 0, remH: 0, awakeH: 0.0,
			want: 0.65, tol: 0.01,
		},
		{
			// Compound bad night: short (5h) + fragmented (1h awake) +
			// no deep + no rem. Stresses every factor at once and
			// pushes the product into the floor-relevant range.
			//   duration  = 5/8        = 0.625
			//   efficiency = 5/(5+1)   = 0.833
			//   structure  = 1 - 0.35  = 0.65
			//   product    ≈ 0.339
			name:   "compound_bad_night",
			totalH: 5.0, deepH: 0, remH: 0, awakeH: 1.0,
			want: 0.625 * (5.0 / 6.0) * 0.65, tol: 0.01,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := SleepQuality(c.totalH, c.deepH, c.remH, c.awakeH)
			approxEq(t, c.name, got, c.want, c.tol)
		})
	}
}

// TestSleepQuality_RangeInvariant: SleepQuality must stay within [0, 1]
// for any plausible input, including weird ones the sensor stack might
// produce. The structure floor + duration clamp + the efficiency ratio
// (always ≤ 1 when awake ≥ 0) are what guarantee this; this test pins
// the property so a future formula tweak can't accidentally break it.
func TestSleepQuality_RangeInvariant(t *testing.T) {
	totals := []float64{0.5, 1, 4, 6, 7.5, 8, 10, 14}
	for _, total := range totals {
		for _, awake := range []float64{0, 0.5, 2, 5} {
			for _, deep := range []float64{0, 0.5, 1.5, 3} {
				for _, rem := range []float64{0, 0.5, 1.5, 3} {
					sq := SleepQuality(total, deep, rem, awake)
					if sq < 0 || sq > 1 {
						t.Fatalf("out of range for total=%v deep=%v rem=%v awake=%v: got %v",
							total, deep, rem, awake, sq)
					}
				}
			}
		}
	}
}

// TestAsymptoticCapacity covers the three load-bearing properties from
// the docstring: no overshoot above 100, signed input handled, sq=0
// produces pure carryover.
func TestAsymptoticCapacity(t *testing.T) {
	cases := []struct {
		name           string
		bank, sq, want float64
	}{
		{"perfect_sleep_full_refill_from_50", 50, 1.0, 100},
		{"perfect_sleep_from_negative", -50, 1.0, 100},
		{"half_sleep_from_zero", 0, 0.5, 50},
		{"zero_sleep_carryover", 73, 0, 73},
		{"zero_sleep_negative_carryover", -20, 0, -20},
		{"already_full_no_overshoot", 100, 1.0, 100},
		{"already_full_partial_no_overshoot", 100, 0.5, 100},
		{"poor_night_from_low", 30, 0.4, 30 + 70*0.4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AsymptoticCapacity(c.bank, c.sq)
			approxEq(t, c.name, got, c.want, eps)
			if got > 100+eps {
				t.Fatalf("overshoot above 100: got %v", got)
			}
		})
	}
}

// TestAsymptoticCapacity_MultiNightAccumulation: the property the
// additive restore lacked. Three poor nights in a row from a starting
// bank of 50 must leave the bank notably lower than three perfect
// nights — this is the test that would have failed under v1.
func TestAsymptoticCapacity_MultiNightAccumulation(t *testing.T) {
	// Three nights of sq=0.5 with constant 30-unit daily drain.
	// Day-by-day: 50 → restore to 75 → drain to 45 → restore to 72.5
	// → drain to 42.5 → restore to 71.25 → drain to 41.25.
	bank := 50.0
	for i := 0; i < 3; i++ {
		bank = AsymptoticCapacity(bank, 0.5)
		bank -= 30
	}
	if bank > 50 {
		t.Fatalf("three poor nights should leave bank below starting 50, got %v", bank)
	}
	// Three perfect nights from same start clearly leave us higher.
	bank2 := 50.0
	for i := 0; i < 3; i++ {
		bank2 = AsymptoticCapacity(bank2, 1.0)
		bank2 -= 30
	}
	if bank2 <= bank {
		t.Fatalf("perfect nights must beat poor nights: perfect=%v poor=%v", bank2, bank)
	}
}

func TestDrainV2(t *testing.T) {
	// Cases written in long form (kcal, sustained, alpha, beta) to
	// document the new four-input shape. v2.2 default deployments
	// pass beta=0 (StressDrainEnabled=false gates EffectiveBeta to
	// 0), so the kcal-only behaviour from v2.0 is the dominant
	// code path — first half of the cases proves equivalence.
	cases := []struct {
		name                          string
		kcal, sustained, alpha, beta  float64
		want                          float64
	}{
		// v2.0 equivalent path (beta=0): same numbers the legacy
		// TestDrainV2 pinned, proving FormulaVersion bump 1→2 doesn't
		// shift bank values for tenants who haven't enabled stress.
		{"v2_default_alpha_typical_day", 500, 0, 0.08, 0, 40},
		{"zero_kcal", 0, 0, 0.08, 0, 0},
		{"negative_kcal_floored", -500, 0, 0.08, 0, 0},
		{"high_load_athlete", 2000, 0, 0.08, 0, 160},
		{"alpha_zero_disables_drain", 1500, 0, 0, 0, 0},
		{"negative_alpha_floored", 1500, 0, -0.08, 0, 0},
		{"both_negative_floored", -1500, 0, -0.08, 0, 0},

		// v2.2 stress term: hr-load contributes only when beta > 0
		// AND sustained_hr_load > 0.
		{"stress_disabled_beta_zero", 500, 7.5, 0.08, 0, 40},  // β=0 → kcal-only
		{"stress_enabled_anxious_day", 500, 7.5, 0.08, 0.8, 46}, // 40 + 0.8·7.5 = 46
		{"stress_enabled_calm_day", 500, 0, 0.08, 0.8, 40},      // load=0 → kcal-only
		{"stress_only_no_kcal", 0, 7.5, 0.08, 0.8, 6},           // 0.8·7.5
		// Negative β would credit the bank for stressful days —
		// same kind of inversion the negative-α guard catches.
		{"negative_beta_floored", 500, 7.5, 0.08, -0.8, 40},
		// Negative load (sensor glitch) shouldn't credit either.
		{"negative_load_floored", 500, -1, 0.08, 0.8, 40},

		// NaN tolerance — important because the orchestrator passes
		// NaN-marked hours (failed per-hour coverage) into
		// SustainedHRLoad, and any downstream NaN must drop the
		// term, not propagate.
		{"nan_kcal_dropped", math.NaN(), 7.5, 0.08, 0.8, 6},
		{"nan_load_dropped", 500, math.NaN(), 0.08, 0.8, 40},
		{"nan_alpha_dropped", 500, 7.5, math.NaN(), 0.8, 6},
		{"nan_beta_dropped", 500, 7.5, 0.08, math.NaN(), 40},
		{"inf_kcal_dropped", math.Inf(1), 7.5, 0.08, 0.8, 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			approxEq(t, c.name, DrainV2(c.kcal, c.sustained, c.alpha, c.beta), c.want, eps)
		})
	}
}

func TestClampSignedBank(t *testing.T) {
	cases := []struct {
		name     string
		in, want float64
	}{
		{"in_range_zero", 0, 0},
		{"in_range_positive", 73, 73},
		{"in_range_negative", -25, -25},
		{"upper_clamp", 150, 100},
		{"lower_clamp", -200, -50},
		{"upper_boundary", 100, 100},
		{"lower_boundary", -50, -50},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			approxEq(t, c.name, ClampSignedBank(c.in), c.want, eps)
		})
	}
}

// TestEndToEndOneDay walks one full day through restore + drain to
// catch wiring mistakes the unit tests don't see (e.g. drain being
// applied to capacity instead of post-clamp bank, or the sign of the
// drain term being flipped). Numbers are checked against a manual
// calculation, not a fixture, so a future formula change requires
// updating this test deliberately.
func TestEndToEndOneDay(t *testing.T) {
	bankYesterday := 35.0
	// total=7, eff=1, deep=1.1/7=0.157 (above 0.15), rem=1.5/7=0.214
	// (above 0.20). Both shortfalls are 0 → structure=1. SQ=7/8=0.875.
	sq := SleepQuality(7.0, 1.1, 1.5, 0.0)
	cap := AsymptoticCapacity(bankYesterday, sq) // 35 + 65·0.875 = 91.875
	drain := DrainV2(700, 0, 0.08, 0)            // 56 — v2.0-equivalent (β=0)
	bankToday := ClampSignedBank(cap - drain)    // 35.875

	approxEq(t, "sq", sq, 0.875, eps)
	approxEq(t, "capacity", cap, 91.875, eps)
	approxEq(t, "drain", drain, 56, eps)
	// Pin the exact final value: 91.875 - 56 = 35.875, well inside
	// [-50, 100] so the clamp is a no-op. A wiring mistake (e.g.,
	// applying drain to the pre-clamp bank, or flipping a sign) would
	// shift this number well outside the eps tolerance.
	approxEq(t, "bank_today", bankToday, 35.875, eps)
}
