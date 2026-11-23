package storage

import (
	"context"
	"fmt"
	"time"
)

// EnergyBank v2 — 14-day forward iteration.
//
// ComputeBankForDate evaluates the dbt solver against this profile's daily
// scores or the already-selected sleep projection. The snapshot writers
// consume its result within their existing transaction and precedence rules.
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
	SleepTotal *float64 // hours
	SleepDeep  *float64 // hours
	SleepRem   *float64 // hours
	SleepAwake *float64 // hours
	ActiveKcal *float64 // kcal
	// A missing §4.4 HR load contributes zero to drain.
	SustainedHRLoad *float64
	// §4.3 flags on the final day are carried into the bank result.
	StressFlags []string
}

const energyWindowDays = 21

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
	if s.sleepRolloutTenant {
		var bank BankResult
		handled, err := s.withActiveSleepRollout(ctx, func(unit *DB, publication SleepRolloutPublication) error {
			if publication.goProjection == nil {
				var err error
				bank, err = unit.ComputeBankForDate(ctx, tz, asOfDate)
				return err
			}
			blocks, _, err := unit.loadReaderSleepWindow(ctx, asOfDate, publication)
			if err != nil {
				return err
			}
			if !hasProjectedEnergyInput(asOfDate, blocks) {
				bank, err = unit.ComputeBankForDate(ctx, tz, asOfDate)
			} else {
				bank, err = unit.computeServedSleepBank(ctx, asOfDate, blocks, unit.GetEnergyConfig())
			}
			return err
		})
		if handled || err != nil {
			return bank, err
		}
	}
	res, _, err := s.readEnergyBankSQL(ctx, asOfDate, s.GetEnergyConfig(), nil)
	return res, err
}

func (s *DB) loadDailyEnergyInputs(ctx context.Context, from, to string) (map[string]dailyInputs, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT date, sleep_total, sleep_deep, sleep_rem, sleep_awake, calories,
		       sustained_hr_load, stress_flags
		FROM daily_scores
		WHERE date >= $1 AND date <= $2`,
		from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byDate := make(map[string]dailyInputs, energyWindowDays)
	for rows.Next() {
		var date string
		var st, sd, sr, sa, kcal, shl *float64
		var sf []string
		if err := rows.Scan(&date, &st, &sd, &sr, &sa, &kcal, &shl, &sf); err != nil {
			return nil, err
		}
		byDate[date] = dailyInputs{
			SleepTotal: st, SleepDeep: sd, SleepRem: sr, SleepAwake: sa,
			ActiveKcal: kcal, SustainedHRLoad: shl, StressFlags: sf,
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return byDate, nil
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
