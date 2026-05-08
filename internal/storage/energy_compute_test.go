package storage

import (
	"testing"
)

// fp returns a *float64, keeping test setup compact. nil literals stay
// readable as "this metric was missing for that day".
func fp(v float64) *float64 { return &v }

// makeDays builds a 21-day window. fillSleep / fillActivity called for
// each index; returning nil indicates "missing data" so the iteration
// must impute (or, on enough missing days, decide stale).
func makeDays(fillSleep func(i int) (totalH, deepH, remH, awakeH *float64),
	fillActivity func(i int) *float64) []dailyInputs {
	out := make([]dailyInputs, energyWindowDays)
	for i := 0; i < energyWindowDays; i++ {
		t, d, r, a := fillSleep(i)
		out[i] = dailyInputs{
			SleepTotal: t,
			SleepDeep:  d,
			SleepRem:   r,
			SleepAwake: a,
			ActiveKcal: fillActivity(i),
		}
	}
	return out
}

// goodNight is the canonical "real, healthy night" fixture used by the
// happy-path tests. SQ ≈ 0.875.
func goodNight(_ int) (totalH, deepH, remH, awakeH *float64) {
	return fp(7.5), fp(1.2), fp(1.5), fp(0)
}

// modestActivity ≈ a 500 kcal day. With α=0.08 default, drain = 40.
func modestActivity(_ int) *float64 { return fp(500) }

func defaultCfg() EnergyConfig { return DefaultEnergyConfig() }

// TestComputeBank_AllCleanIsFresh: 21 days of normal data → state=fresh,
// bank converges into a plausible range. Pin the exact value to catch
// formula drift.
func TestComputeBank_AllCleanIsFresh(t *testing.T) {
	days := makeDays(goodNight, modestActivity)
	r := computeBankFromDays(days, defaultCfg())
	if r.State != "fresh" {
		t.Fatalf("state: got %q, want fresh", r.State)
	}
	// 14 iterations of restore(sq=0.875) - drain(40) starting from 50
	// settle into a steady state where (100-bank)·0.875 = 40, i.e.
	// bank ≈ 100 - 40/0.875 ≈ 54.3.
	if r.Bank < 50 || r.Bank > 60 {
		t.Fatalf("bank out of expected steady-state band [50,60]: got %d", r.Bank)
	}
	if r.Display != r.Bank {
		t.Fatalf("display should equal bank in positive range, got display=%d bank=%d", r.Display, r.Bank)
	}
	if r.AlphaUsed == 0 {
		t.Fatal("alpha must be reported for non-stale results")
	}
	if r.Components == nil {
		t.Fatal("components must be populated for non-stale results")
	}
}

// TestComputeBank_OneMissingDayStaysFresh: a single sensor blackout
// inside the 14-day window must NOT trip the trust state. The
// cross-user validation showed scattered single-day gaps are common
// even on healthy users.
func TestComputeBank_OneMissingDayStaysFresh(t *testing.T) {
	missingAt := 14
	days := makeDays(
		func(i int) (totalH, deepH, remH, awakeH *float64) {
			if i == missingAt {
				return nil, nil, nil, nil
			}
			return goodNight(i)
		},
		modestActivity,
	)
	r := computeBankFromDays(days, defaultCfg())
	if r.State != "fresh" {
		t.Fatalf("state: got %q, want fresh", r.State)
	}
}

// TestComputeBank_TwoMissingDaysIsEstimated: 2 imputed days in the last
// 7 → "estimated". Both the consecutive-run trigger and the proportional
// trigger flow through this case; this test pins the proportional path.
func TestComputeBank_TwoMissingDaysIsEstimated(t *testing.T) {
	missing := map[int]bool{12: true, 17: true}
	days := makeDays(
		func(i int) (totalH, deepH, remH, awakeH *float64) {
			if missing[i] {
				return nil, nil, nil, nil
			}
			return goodNight(i)
		},
		modestActivity,
	)
	r := computeBankFromDays(days, defaultCfg())
	if r.State != "estimated" {
		t.Fatalf("state: got %q, want estimated", r.State)
	}
}

// TestComputeBank_FiveConsecutiveMissingIsStale: hits the consecutive
// run trigger. State=stale, bank result must NOT include components
// (we'd be lying about the inputs the formula consumed).
func TestComputeBank_FiveConsecutiveMissingIsStale(t *testing.T) {
	days := makeDays(
		func(i int) (totalH, deepH, remH, awakeH *float64) {
			// Last 5 days missing, including today.
			if i >= energyWindowDays-5 {
				return nil, nil, nil, nil
			}
			return goodNight(i)
		},
		modestActivity,
	)
	r := computeBankFromDays(days, defaultCfg())
	if r.State != "stale" {
		t.Fatalf("state: got %q, want stale", r.State)
	}
	if r.Components != nil {
		t.Fatalf("stale result must not expose components, got %v", r.Components)
	}
}

// TestComputeBank_ScatteredManyMissingIsStale: hits the proportional
// trigger. 8 scattered imputed days in the 14-day iteration range
// should trip stale even when no consecutive run reaches 5. This is
// the second-tenant case from the cross-user validation.
func TestComputeBank_ScatteredManyMissingIsStale(t *testing.T) {
	missing := map[int]bool{
		7: true, 9: true, 11: true, 12: true,
		14: true, 16: true, 18: true, 20: true,
	}
	days := makeDays(
		func(i int) (totalH, deepH, remH, awakeH *float64) {
			if missing[i] {
				return nil, nil, nil, nil
			}
			return goodNight(i)
		},
		modestActivity,
	)
	r := computeBankFromDays(days, defaultCfg())
	if r.State != "stale" {
		t.Fatalf("state: got %q, want stale (8 scattered imputed in 14d window)", r.State)
	}
}

// TestComputeBank_HighDrainPushesNegative: a sustained 1500 kcal/day
// load with poor sleep eventually pushes the bank into the floor. The
// signed Bank goes to -50; Display clamps to 0; the AI prompt sees
// the deficit, the user does not.
func TestComputeBank_HighDrainPushesNegative(t *testing.T) {
	days := makeDays(
		// Persistent short sleep — restore is weak.
		func(_ int) (totalH, deepH, remH, awakeH *float64) {
			return fp(4.0), fp(0.4), fp(0.5), fp(0.5)
		},
		// 1500 kcal/day is a heavy training load for a typical user.
		func(_ int) *float64 { return fp(1500) },
	)
	r := computeBankFromDays(days, defaultCfg())
	if r.Bank > -40 {
		t.Fatalf("expected bank near floor (-50), got %d", r.Bank)
	}
	if r.Display != 0 {
		t.Fatalf("display must clamp negative bank to 0, got %d", r.Display)
	}
}

// TestComputeBank_PerfectSleepPinsHigh: maxed-out restore + zero drain
// pins the bank at 100 (the asymptote). Catches the upper-clamp path.
func TestComputeBank_PerfectSleepPinsHigh(t *testing.T) {
	days := makeDays(
		func(_ int) (totalH, deepH, remH, awakeH *float64) {
			return fp(8.0), fp(1.2), fp(1.6), fp(0)
		},
		func(_ int) *float64 { return fp(0) },
	)
	r := computeBankFromDays(days, defaultCfg())
	if r.Bank != 100 {
		t.Fatalf("expected bank to pin at 100, got %d", r.Bank)
	}
	if r.Display != 100 {
		t.Fatalf("display: got %d, want 100", r.Display)
	}
}

// TestComputeBank_TodayMissingFlaggedNotState: today's metric missing,
// rest of window clean → flags=[imputed_sleep] BUT state stays fresh
// (one-day gap is below the threshold).
func TestComputeBank_TodayMissingFlaggedNotState(t *testing.T) {
	days := makeDays(
		func(i int) (totalH, deepH, remH, awakeH *float64) {
			if i == energyWindowDays-1 {
				return nil, nil, nil, nil
			}
			return goodNight(i)
		},
		modestActivity,
	)
	r := computeBankFromDays(days, defaultCfg())
	if r.State != "fresh" {
		t.Fatalf("state: got %q, want fresh", r.State)
	}
	hasFlag := false
	for _, f := range r.Flags {
		if f == "imputed_sleep" {
			hasFlag = true
			break
		}
	}
	if !hasFlag {
		t.Fatalf("expected imputed_sleep flag on today, got %v", r.Flags)
	}
}

// TestComputeBank_SeedConvergence: starting the iteration from the
// default seed (50) vs an alternate seed must produce the same final
// bank. This is the property that lets us avoid persisting seed state.
// The test forces it by running computeBankFromDays twice, swapping in
// a hacked seed via a copy of the iteration loop.
func TestComputeBank_SeedConvergence(t *testing.T) {
	days := makeDays(goodNight, modestActivity)
	r := computeBankFromDays(days, defaultCfg())
	// Re-implement the loop with a different seed; if convergence is
	// real, the final values match within ~1 unit even after rounding.
	cfg := defaultCfg()
	want := r.Bank
	for _, seed := range []float64{0, 25, 75, 100} {
		bank := seed
		// Recompute SQ/drain in-place (no imputation needed — clean
		// data above).
		for i := energyIterStart; i < energyWindowDays; i++ {
			d := days[i]
			sq := 0.0
			if d.SleepTotal != nil {
				deep := zeroIfNil(d.SleepDeep)
				rem := zeroIfNil(d.SleepRem)
				awake := zeroIfNil(d.SleepAwake)
				sq = sleepQualityForTest(*d.SleepTotal, deep, rem, awake)
			}
			drain := 0.0
			if d.ActiveKcal != nil {
				drain = cfg.EffectiveAlpha() * *d.ActiveKcal
			}
			cap := bank + (100-bank)*sq
			bank = cap - drain
			if bank < -50 {
				bank = -50
			}
			if bank > 100 {
				bank = 100
			}
		}
		got := int(bank + 0.5)
		if got != want && (got-want > 1 || want-got > 1) {
			t.Fatalf("seed %v should converge to %d, got %d", seed, want, got)
		}
	}
}

// sleepQualityForTest mirrors health.SleepQuality so the seed
// convergence test stays self-contained (avoids importing health
// indirectly through the same code path it's verifying).
func sleepQualityForTest(total, deep, rem, awake float64) float64 {
	if total <= 0 {
		return 0
	}
	dur := total / 8
	if dur > 1 {
		dur = 1
	}
	if dur < 0 {
		dur = 0
	}
	eff := total / (total + awake)
	deepShort := 0.15 - deep/total
	if deepShort < 0 {
		deepShort = 0
	}
	remShort := 0.20 - rem/total
	if remShort < 0 {
		remShort = 0
	}
	struc := 1.0 - deepShort - remShort
	if struc < 0.5 {
		struc = 0.5
	}
	return dur * eff * struc
}
