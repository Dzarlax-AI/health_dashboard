package storage

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"health-receiver/internal/health"
)

// EnergyBank v2 — 14-day forward iteration.
//
// This file is read-only: it pulls 21 days from daily_scores, runs the
// pure-math kernel from internal/health/energy_v2.go, and returns
// today's bank. It does NOT write to energy_snapshots — persistence
// arrives in PR5.
//
// The 21-day window splits as follows:
//
//	indices  0-6   imputation lookback only (sleep/activity sampled,
//	               but bank iteration does not visit them)
//	indices  7-20  bank iteration (14 days, last index = today)
//
// Bootstrap: the iteration starts from a hard-coded seed of 50; the
// asymptotic restore is a contraction mapping that empirically forgets
// the seed within ~7 days, so by index 14 the seed contributes <0.005
// and by index 20 it is bit-identical across any reasonable seed.
// Persisting seed state would just lock in floating-point noise.

// BankResult is the read-side projection of today's EnergyBank v2 state.
//
// TodayDrain and TodayRestore feed energy_snapshots.drain_delta and
// restore_delta respectively. v2.0 integrates the formula on a daily
// granularity, so the per-day deltas and the per-5-min-bucket deltas
// are the same value within a day; if v2.1 moves to true intra-day
// integration these become per-bucket and the schema doesn't change.
type BankResult struct {
	// Bank is the signed bank, clamped to [-50, 100]. Stored signed in
	// the DB so the AI prompt can frame a sustained deficit ("you're
	// in the hole"); the UI uses Display instead.
	Bank int
	// Display is Bank clamped to [0, 100]. The user never sees minus
	// numbers — the signed signal is for the AI prompt path only.
	Display int
	// State summarises trust in the result: "fresh" (≤1 imputed day in
	// the last 7), "estimated" (2-4 consecutive imputed OR 2-4 in the
	// 14-day window), or "stale" (≥5 consecutive imputed OR >7 in the
	// 14-day window). On "stale" the caller should suppress the bank
	// number and surface a sensor-sync placeholder.
	State string
	// Flags are non-state metadata about today specifically — e.g.
	// "imputed_sleep" if today's sleep was estimated. Distinct from
	// State (which summarises the whole window).
	Flags []string
	// FormulaVersion is stamped on the result so callers can detect a
	// version change and refresh.
	FormulaVersion int
	// AlphaUsed is the effective drain coefficient (base * factor) for
	// debugging. Zero when the bank was not actually computed (stale).
	AlphaUsed float64
	// TodayDrain and TodayRestore are the per-day drain and restore
	// magnitudes for today's iteration step, rounded to int. Populated
	// for non-stale results; zero when stale (we suppress all
	// numerical signal in that case).
	TodayDrain   int
	TodayRestore int
	// Components is an optional audit trail. Currently exposes today's
	// raw inputs so the dashboard `<details>` view can show "what the
	// formula saw"; nil when unavailable.
	Components map[string]float64
}

// dailyInputs is the per-day raw data the iteration consumes. All
// pointer fields are nil when the corresponding metric was absent for
// that day; the iteration interprets nil as "needs imputation" (or
// "skip" for days outside the iteration range).
type dailyInputs struct {
	SleepTotal      *float64 // hours
	SleepDeep       *float64 // hours
	SleepRem        *float64 // hours
	SleepAwake      *float64 // hours
	ActiveKcal      *float64 // kcal
	SustainedHRLoad *float64 // §4.4 z-load, cached in daily_scores; NULL means
	// the coverage / calibration gates fired for the day. zeroIfNil
	// in computeBankFromDays converts that to 0 so DrainV2's β term
	// contributes 0 (same shape as a low-stress day) — falls back
	// cleanly to v2.0 drain without flagging the day as imputed.
	StressFlags []string // §4.3 stratified flags, cached in
	// daily_scores.stress_flags. Currently the HR-z-derived subset:
	// stale_stress / calibration_warmup / acute_stress /
	// sustained_load. Multi-channel flags (illness_signature,
	// recovery_debt, parasympathetic_rebound) land in a follow-up
	// PR. Surfaced into BankResult.Flags for *today only* by
	// computeBankFromDays.
}

const (
	// energyWindowDays is the total lookback (iteration + imputation).
	energyWindowDays = 21
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

// ComputeBankForToday pulls the 21-day window from daily_scores in the
// tenant's TZ, runs the forward iteration, and returns today's bank.
// Tenant TZ comes from the same source as the energy_snapshots writer
// (PR1): caller passes it explicitly so this function stays a pure
// computation over inputs.
//
// A bad TZ is propagated as an error rather than silently coerced to
// UTC — the bank is materially time-sensitive (off-by-one-day on
// midnight boundaries) and a misconfigured tenant should fail loud
// rather than report yesterday's number as today's. This is the
// opposite policy from the energy_snapshots writer, which falls back
// to UTC because writing yesterday's snapshot under a UTC date is
// recoverable next ingest.
func (s *DB) ComputeBankForToday(ctx context.Context, tz string) (BankResult, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return BankResult{}, fmt.Errorf("load tenant TZ %q: %w", tz, err)
	}
	today := time.Now().In(loc).Format("2006-01-02")
	return s.ComputeBankForDate(ctx, tz, today)
}

// ComputeBankForDate runs the v2 iteration ending at `asOfDate`
// (YYYY-MM-DD in tenant TZ). Shared by the live orchestrator (with
// today's date) and the cmd/energy_backfill CLI (with historical
// dates). Pure read against daily_scores — no writes.
//
// The `tz` arg is parsed only to validate it loud-fails on a typo,
// matching ComputeBankForToday's "fail loud on bad TZ" policy. The
// 21-day window arithmetic happens on the date string directly via
// subtractDays so DST shifts and leap seconds don't drift the lookback.
func (s *DB) ComputeBankForDate(ctx context.Context, tz, asOfDate string) (BankResult, error) {
	if _, err := time.LoadLocation(tz); err != nil {
		return BankResult{}, fmt.Errorf("load tenant TZ %q: %w", tz, err)
	}
	if _, err := time.Parse("2006-01-02", asOfDate); err != nil {
		return BankResult{}, fmt.Errorf("parse asOfDate %q: %w", asOfDate, err)
	}
	startDate := subtractDays(asOfDate, energyWindowDays-1)

	rows, err := s.pool.Query(ctx, `
		SELECT date, sleep_total, sleep_deep, sleep_rem, sleep_awake, calories,
		       sustained_hr_load, stress_flags
		FROM daily_scores
		WHERE date >= $1 AND date <= $2`,
		startDate, asOfDate)
	if err != nil {
		return BankResult{}, err
	}
	defer rows.Close()

	byDate := make(map[string]dailyInputs, energyWindowDays)
	for rows.Next() {
		var date string
		var st, sd, sr, sa, kcal, shl *float64
		var sf []string
		if err := rows.Scan(&date, &st, &sd, &sr, &sa, &kcal, &shl, &sf); err != nil {
			return BankResult{}, err
		}
		byDate[date] = dailyInputs{
			SleepTotal: st, SleepDeep: sd, SleepRem: sr, SleepAwake: sa,
			ActiveKcal: kcal, SustainedHRLoad: shl, StressFlags: sf,
		}
	}
	if err := rows.Err(); err != nil {
		return BankResult{}, err
	}

	days := make([]dailyInputs, energyWindowDays)
	for i := 0; i < energyWindowDays; i++ {
		date := subtractDays(asOfDate, energyWindowDays-1-i)
		days[i] = byDate[date]
	}

	cfg := s.GetEnergyConfig()
	return computeBankFromDays(days, cfg), nil
}

// computeBankFromDays is the pure iteration: takes a length-21 slice
// of daily inputs, returns today's bank result. Extracted so unit tests
// can exercise the imputation / trust-state logic without a database.
func computeBankFromDays(days []dailyInputs, cfg EnergyConfig) BankResult {
	if len(days) != energyWindowDays {
		return BankResult{State: "stale", FormulaVersion: cfg.FormulaVersion}
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
				windowMedianFloat(sq, imputedSleep))
		}
		if imputedActivity[i] {
			drain[i] = trailingAvg(drain, imputedActivity, i, energyMinValidLookback,
				windowMedianFloat(drain, imputedActivity))
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
	return res
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

// windowMedianFloat is the median of non-imputed values across the full
// 21-day window. Used as the imputation fallback when the trailing
// lookback runs out of non-imputed neighbours. Zero on an empty window
// (which only happens in the all-missing case where the trust state is
// already "stale").
func windowMedianFloat(values []float64, imputed []bool) float64 {
	xs := make([]float64, 0, len(values))
	for i, v := range values {
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

func zeroIfNil(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func clampDisplay(bank int) int {
	if bank < 0 {
		return 0
	}
	if bank > 100 {
		return 100
	}
	return bank
}
