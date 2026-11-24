package storage

import (
	"context"
	"encoding/json"

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

const energyPacketSQL = `jsonb_build_object('run',(select to_jsonb(er)-'profile'-'capture_id' from energy_run er),
    'days',(select coalesce(jsonb_agg(to_jsonb(d)-'profile'-'capture_id' order by day),'[]'::jsonb) from energy_days d))`

var energySolverCTE = `), energy_result as (` + dbtmetrics.EnergyBankSQL("energy_run", "energy_days") + `)
	`

const energyResultSQL = ` select signed_bank, display_bank, state, flags, formula_version, alpha_used,
	       drain_delta, restore_delta, components, unrounded_bank, effective_quality, unrounded_restore, incoming, before_clamp`

func (s *DB) readEnergyBankSQL(ctx context.Context, date string, cfg EnergyConfig, selected map[string]dailyInputs) (BankResult, energyTrace, error) {
	type result struct {
		bank  BankResult
		trace energyTrace
	}
	r, err := withDecisionJournal(ctx, s, func(db *DB, journal bool) (result, error) {
		b, t, err := db.evaluateEnergyBankSQL(ctx, date, cfg, selected, journal)
		return result{b, t}, err
	})
	return r.bank, r.trace, err
}

func (s *DB) evaluateEnergyBankSQL(ctx context.Context, date string, cfg EnergyConfig, selected map[string]dailyInputs, journal bool) (BankResult, energyTrace, error) {
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
	// Compare the exact input packet and fetch the published answer in one snapshot.
	// The trace also feeds the optional nap calculation and must match the same solver run.
	type publishedBank struct {
		BankResult
		Trace []float64
	}
	published, found, err := withPublishedDecision(ctx, s, func(db *DB) (publishedBank, bool, error) {
		var answer publishedBank
		q := energyRunSQL + source + `) select r.result from hdbt_decision_results r
            where r.tableoid=to_regclass(format('%I.hdbt_decision_results',current_schema()))
            and (` + decisionPublishedReadPredicate + `) and r.metric='energy_bank'
            and r.decision_key=$1::text and r.formula_version=$2::int
            and r.input_hash=encode(sha256(convert_to(jsonb_build_array($2::int,` + energyPacketSQL + `)::text,'UTF8')),'hex')`
		err := db.pool.QueryRow(ctx, q, args...).Scan(&answer)
		return answer, len(answer.Trace) == 5, err
	})
	if err != nil {
		return BankResult{}, energyTrace{}, err
	}
	if found {
		if len(published.Flags) == 0 {
			published.Flags = nil
		}
		t := published.Trace
		return published.BankResult, energyTrace{bank: t[0], quality: t[1], restore: t[2], incoming: t[3], beforeClamp: t[4]}, nil
	}
	var res BankResult
	var trace energyTrace
	columns := []any{&res.Bank, &res.Display, &res.State, &res.Flags, &res.FormulaVersion, &res.AlphaUsed,
		&res.TodayDrain, &res.TodayRestore, &res.Components, &trace.bank, &trace.quality, &trace.restore, &trace.incoming, &trace.beforeClamp}
	query := energyRunSQL + source + energySolverCTE
	if journal {
		query += `, decision_packets as (
            select 'energy_bank'::text as metric, r.target_day::text as decision_key, r.formula_version,
                ` + energyPacketSQL + ` as inputs,
                jsonb_build_object('Bank',r.signed_bank,'Display',r.display_bank,'State',r.state,
                    'Flags',r.flags,'FormulaVersion',r.formula_version,'AlphaUsed',r.alpha_used,
                    'TodayDrain',r.drain_delta,'TodayRestore',r.restore_delta,'Components',r.components) as result
            from energy_result r
        )` + decisionJournalInsertSQL
	}
	query += energyResultSQL
	var packet json.RawMessage
	capture, _ := ctx.Value(localEnergyCaptureKey{}).(*LocalEnergyDecision)
	if capture != nil && s.profileGraphSchema != "" {
		query += `, jsonb_build_object('run',(select to_jsonb(r)-'profile'-'capture_id' from energy_run r),
            'days',(select coalesce(jsonb_agg(to_jsonb(d)-'profile'-'capture_id' order by day),'[]'::jsonb) from energy_days d),
            'database',current_database(),'role',current_user,'source_schema',current_schema(),
            'captured_at',statement_timestamp())`
		columns = append(columns, &packet)
	}
	err = s.pool.QueryRow(ctx, query+" from energy_result", args...).Scan(columns...)
	if err != nil {
		return BankResult{}, energyTrace{}, err
	}
	if len(res.Flags) == 0 {
		res.Flags = nil
	}
	if capture != nil && packet != nil {
		capture.Inputs = packet
		capture.Result = res
	}
	return res, trace, nil
}
