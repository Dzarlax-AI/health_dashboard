package storage

import (
	"context"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
)

// One profile and one dated run are already fixed by the authenticated DB and
// the caller. Apply the same solver that dbt uses for a set of captured runs.
const energyRunSQL = `with energy_run as (
	select ''::text as profile, ''::text as capture_id, $1::text::date as target_day,
	       'current'::text as snapshot_kind, $2::int as formula_version,
	       $3::float8 as alpha, $4::float8 as factor,
	       $5::bool as stress_enabled, $6::float8 as beta
), energy_days as (`

// Use the text representation that the original pgx daily_scores reader exposed
// when promoting the single-precision columns to float64.
const energyDailyScoresSQL = `select ''::text as profile, ''::text as capture_id,
	date::date as day, sleep_total::text::float8 as sleep_total,
	sleep_deep::text::float8 as deep, sleep_rem::text::float8 as rem,
	sleep_awake::text::float8 as awake, calories::text::float8 as active_kcal,
	sustained_hr_load::text::float8 as hr_load, stress_flags
	from daily_scores
	where date between ($1::text::date-20)::text and $1::text`

// The published sleep projection and the local rebuild have already selected
// their own authoritative inputs. Preserve nil measurements and the dated flags
// rather than re-reading newer data after the caller's selection.
const energySelectedDaysSQL = `select ''::text as profile, ''::text as capture_id,
	($1::text::date-21+i::int) as day, sleep_total, deep, rem, awake, active_kcal, hr_load,
	case when i=21 then $13::text[] end as stress_flags
	from unnest($7::float8[], $8::float8[], $9::float8[], $10::float8[], $11::float8[], $12::float8[])
	with ordinality as r(sleep_total,deep,rem,awake,active_kcal,hr_load,i)`

var energySolverSQL = `), energy_result as (` + dbtmetrics.EnergyBankSQL("energy_run", "energy_days") + `)
	select signed_bank, display_bank, state, flags, formula_version, alpha_used,
	       drain_delta, restore_delta, components, unrounded_bank, effective_quality, unrounded_restore, incoming, before_clamp from energy_result`

func (s *DB) readEnergyBankSQL(ctx context.Context, date string, cfg EnergyConfig, selected map[string]dailyInputs) (BankResult, energyTrace, error) {
	args := []any{date, cfg.FormulaVersion, cfg.Alpha, cfg.AlphaFactor, cfg.StressDrainEnabled, cfg.Beta}
	source := energyDailyScoresSQL
	if selected != nil {
		source = energySelectedDaysSQL
		var columns [6][]*float64
		for i := 0; i < energyWindowDays; i++ {
			d := selected[subtractDays(date, energyWindowDays-1-i)]
			for col, value := range [...]*float64{d.SleepTotal, d.SleepDeep, d.SleepRem, d.SleepAwake, d.ActiveKcal, d.SustainedHRLoad} {
				columns[col] = append(columns[col], value)
			}
		}
		for _, column := range columns {
			args = append(args, column)
		}
		args = append(args, selected[date].StressFlags)
	}
	var res BankResult
	var trace energyTrace
	err := s.pool.QueryRow(ctx, energyRunSQL+source+energySolverSQL, args...).Scan(
		&res.Bank, &res.Display, &res.State, &res.Flags, &res.FormulaVersion, &res.AlphaUsed,
		&res.TodayDrain, &res.TodayRestore, &res.Components, &trace.bank, &trace.quality, &trace.restore, &trace.incoming, &trace.beforeClamp)
	if err != nil {
		return BankResult{}, energyTrace{}, err
	}
	if len(res.Flags) == 0 {
		res.Flags = nil
	}
	return res, trace, nil
}
