package storage

// The previous production solver remains an independent regression oracle for
// the dbt definition and its runtime input adapter.
import (
	"math"
	"sort"

	"health-receiver/internal/health"
)

const (
	// energyIterStart is the first index actually fed to the bank
	// formula. Indices below it serve only as imputation lookback.
	energyIterStart = 7
	// energyMinValidLookback is the minimum number of non-imputed
	// neighbours required to trust a trailing average. Below this we
	// fall back to a window-wide median (and, downstream, the trust
	// state will trip to "estimated" or "stale").
	energyMinValidLookback = 3
	// energySeed is the bootstrap value. The asymptotic restore is a
	// contraction mapping; empirically the seed is forgotten within
	// ~7 days, so any value in [0, 100] gives the same final result.
	energySeed = 50.0
)

func computeCalendarEnergyBank(byDate map[string]dailyInputs, asOfDate string, cfg EnergyConfig) BankResult {
	result, _ := calculateCalendarEnergyBank(byDate, asOfDate, cfg)
	return result
}

func calculateCalendarEnergyBank(byDate map[string]dailyInputs, asOfDate string, cfg EnergyConfig) (BankResult, float64) {
	days := make([]dailyInputs, energyWindowDays)
	for i := 0; i < energyWindowDays; i++ {
		date := subtractDays(asOfDate, energyWindowDays-1-i)
		days[i] = byDate[date]
	}

	return calculateBankFromDays(days, cfg)
}

// computeBankFromDays is the pure iteration: takes a length-21 slice
// of daily inputs, returns today's bank result. Extracted so unit tests
// can exercise the imputation / trust-state logic without a database.
func computeBankFromDays(days []dailyInputs, cfg EnergyConfig) BankResult {
	result, _ := calculateBankFromDays(days, cfg)
	return result
}

func calculateBankFromDays(days []dailyInputs, cfg EnergyConfig) (BankResult, float64) {
	if len(days) != energyWindowDays {
		return BankResult{State: "stale", FormulaVersion: cfg.FormulaVersion}, 0
	}

	// Pre-compute "raw" sleep quality and drain for every day. For days
	// where the underlying metric is absent the value is left at its
	// zero default and the corresponding imputed flag is true; the
	// iteration step below substitutes a trailing average.
	sq := make([]float64, energyWindowDays)
	drain := make([]float64, energyWindowDays)
	imputedSleep := make([]bool, energyWindowDays)
	imputedActivity := make([]bool, energyWindowDays)

	for i := 0; i < energyWindowDays; i++ {
		d := days[i]
		// Require ALL stage columns present, not just sleep_total. A
		// partial row (total recorded but stages absent) would feed
		// zeros into health.SleepQuality, falsely tripping the
		// structure penalty (deep_pct=0 → shortfall=0.15) on a night
		// that almost certainly had real deep/REM the sensor stack
		// just failed to write. Better to mark imputed and pull a
		// trailing average — the formula's response to partial data
		// should match its response to fully-missing data, not a
		// pessimistic interpretation of "stages were really zero".
		if d.SleepTotal != nil && *d.SleepTotal > 0 &&
			d.SleepDeep != nil && d.SleepRem != nil && d.SleepAwake != nil {
			sq[i] = health.SleepQuality(*d.SleepTotal, *d.SleepDeep, *d.SleepRem, *d.SleepAwake)
		} else {
			imputedSleep[i] = true
		}
		if d.ActiveKcal != nil && *d.ActiveKcal >= 0 {
			// v2.2: pass the per-day sustained_hr_load cached in
			// daily_scores.sustained_hr_load by the
			// upsertSustainedHRLoadForDate writer. NULL → 0 here;
			// the EffectiveBeta gate keeps β=0 until the §4.5
			// validation rubric clears the tenant, so the new
			// term contributes 0 regardless of the load value.
			drain[i] = health.DrainV2(
				*d.ActiveKcal,
				zeroIfNil(d.SustainedHRLoad),
				cfg.EffectiveAlpha(),
				cfg.EffectiveBeta(),
			)
		} else {
			imputedActivity[i] = true
		}
	}

	// Run imputation: for every iteration-range day with a missing
	// metric, replace its value with the trailing 7-day average of
	// non-imputed neighbours. Cascade-aware — once we impute day i, day
	// i+1's lookback excludes it, so a long gap doesn't drift toward
	// the last real value.
	for i := energyIterStart; i < energyWindowDays; i++ {
		if imputedSleep[i] {
			sq[i] = trailingAvg(sq, imputedSleep, i, energyMinValidLookback,
				prefixMedianFloat(sq, imputedSleep, i))
		}
		if imputedActivity[i] {
			drain[i] = trailingAvg(drain, imputedActivity, i, energyMinValidLookback,
				prefixMedianFloat(drain, imputedActivity, i))
		}
	}

	// Forward-iterate the bank. clampSignedBank lives in the math
	// kernel; we lift the result to int after the loop so intermediate
	// drift stays in float64. Snapshot today's restore (cap −
	// bank_before) and drain so the persisted energy_snapshots row
	// can carry meaningful per-day deltas.
	bank := energySeed
	var todayRestore, todayDrain float64
	for i := energyIterStart; i < energyWindowDays; i++ {
		bankBefore := bank
		cap := health.AsymptoticCapacity(bankBefore, sq[i])
		bank = health.ClampSignedBank(cap - drain[i])
		if i == energyWindowDays-1 {
			todayRestore = cap - bankBefore
			todayDrain = drain[i]
		}
	}

	state := trustState(imputedSleep, imputedActivity)
	flags := todayFlags(imputedSleep, imputedActivity)
	// Surface today's §4.3 stress flags alongside the v2.0 imputed
	// flags. Both feed the same energy_snapshots.flags TEXT[]
	// column, so consumers (briefing.go, future verdict layer)
	// read them with one query. Cached on daily_scores by the
	// orchestrator — no recompute at read time.
	if today := days[energyWindowDays-1].StressFlags; len(today) > 0 {
		flags = append(flags, today...)
	}

	res := BankResult{
		Bank:           int(math.Round(bank)),
		Display:        clampDisplay(int(math.Round(bank))),
		State:          state,
		Flags:          flags,
		FormulaVersion: cfg.FormulaVersion,
		AlphaUsed:      cfg.EffectiveAlpha(),
	}
	// Today's audit trail and per-day deltas are exposed only when we
	// actually rendered a number — "stale" suppresses the bank, so
	// showing components or deltas would lie about what the formula
	// consumed.
	if state != "stale" {
		res.Components = todayComponents(days[energyWindowDays-1], sq[energyWindowDays-1], drain[energyWindowDays-1])
		res.TodayDrain = int(math.Round(todayDrain))
		res.TodayRestore = int(math.Round(todayRestore))
	}
	return res, bank
}

// trailingAvg averages values[i-7..i-1] over indices that are NOT
// imputed. When fewer than minValid such indices exist, returns the
// supplied fallback (typically a window-wide median, which the caller
// pre-computes once per imputation pass).
func trailingAvg(values []float64, imputed []bool, i, minValid int, fallback float64) float64 {
	start := i - 7
	if start < 0 {
		start = 0
	}
	var sum float64
	var n int
	for j := start; j < i; j++ {
		if imputed[j] {
			continue
		}
		sum += values[j]
		n++
	}
	if n < minValid {
		return fallback
	}
	return sum / float64(n)
}

// prefixMedianFloat is a causal fallback: a historical day may use only
// observations strictly before it, never data from its future.
func prefixMedianFloat(values []float64, imputed []bool, before int) float64 {
	xs := make([]float64, 0, before)
	for i, v := range values[:before] {
		if !imputed[i] {
			xs = append(xs, v)
		}
	}
	if len(xs) == 0 {
		return 0
	}
	sort.Float64s(xs)
	mid := len(xs) / 2
	if len(xs)%2 == 0 {
		return (xs[mid-1] + xs[mid]) / 2
	}
	return xs[mid]
}

// trustState classifies the iteration window for caller rendering.
// Two parallel triggers (consecutive run from "today" backward, and
// proportional count over the 14-day iteration range) catch two
// different failure modes: a clean stretch of bad sync vs scattered
// gaps. Either trigger alone wouldn't have caught the cross-user
// validation cases (see ENERGY_BANK.md § Missing-data handling).
func trustState(imputedSleep, imputedActivity []bool) string {
	consecutive := 0
	for i := energyWindowDays - 1; i >= energyIterStart; i-- {
		if imputedSleep[i] || imputedActivity[i] {
			consecutive++
		} else {
			break
		}
	}
	totalIter := 0
	for i := energyIterStart; i < energyWindowDays; i++ {
		if imputedSleep[i] || imputedActivity[i] {
			totalIter++
		}
	}
	last7Imputed := 0
	for i := energyWindowDays - 7; i < energyWindowDays; i++ {
		if imputedSleep[i] || imputedActivity[i] {
			last7Imputed++
		}
	}

	switch {
	case consecutive >= 5 || totalIter > 7:
		return "stale"
	case consecutive >= 2 || totalIter >= 2 || last7Imputed >= 2:
		return "estimated"
	default:
		return "fresh"
	}
}

// todayFlags surfaces "today specifically was imputed" so the UI can
// dot the latest bucket on the sparkline. Distinct from BankResult.State
// which characterises the whole iteration window.
func todayFlags(imputedSleep, imputedActivity []bool) []string {
	var flags []string
	last := energyWindowDays - 1
	if imputedSleep[last] {
		flags = append(flags, "imputed_sleep")
	}
	if imputedActivity[last] {
		flags = append(flags, "imputed_activity")
	}
	return flags
}

func todayComponents(d dailyInputs, sq, drain float64) map[string]float64 {
	c := map[string]float64{
		"sleep_quality": sq,
		"drain":         drain,
	}
	if d.SleepTotal != nil {
		c["sleep_total_h"] = *d.SleepTotal
	}
	if d.ActiveKcal != nil {
		c["active_kcal"] = *d.ActiveKcal
	}
	// v2.2 audit trail — ALWAYS write the z-load when the cache has
	// it, even when EffectiveBeta=0 keeps the term out of drain.
	// Per STRESS_MEASUREMENT.md §6 Q3: "compute sustained_hr_load_z
	// into `components` for audit, but β_effective = 0 and the
	// bank does not move on the new term". This is how the
	// future calibration UI (PR-11) reads a tenant's z-load
	// history without having to re-run the orchestrator.
	if d.SustainedHRLoad != nil {
		c["sustained_hr_load_z"] = *d.SustainedHRLoad
	}
	return c
}
