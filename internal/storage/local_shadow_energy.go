package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

type localShadowCurrentEnergy struct {
	date  string
	state string
	LatestEnergySnapshot
}

// GetLocalShadowEnergyBriefing uses the current result for the configured local
// profile and tenant date. Previously recorded snapshots remain historical.
func (s *DB) GetLocalShadowEnergyBriefing(ctx context.Context, schema, profile, lang string, now time.Time) (*health.BriefingResponse, error) {
	date := now.In(s.reportTZLocation()).Format(isoDate)
	current := localShadowCurrentEnergy{date: date}
	err := s.pool.QueryRow(ctx, `SELECT state,bank,drain_delta,restore_delta,formula_version,flags
		FROM sleep_shadow.energy_current WHERE profile=$1 AND tenant_schema=$2 AND local_date=$3::text::date`,
		profile, schema, date).Scan(&current.state, &current.Bank, &current.DrainDelta, &current.RestoreDelta,
		&current.FormulaVersion, &current.Flags)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.GetHealthBriefing(lang)
	}
	if err != nil {
		return nil, err
	}
	return s.getHealthBriefing(lang, &current, nil)
}

// RefreshLocalShadowEnergy publishes the current result, repairs synthetic
// history and records the current bucket if it is free, in one transaction.
func RefreshLocalShadowEnergy(ctx context.Context, tx pgx.Tx, schema, profile string, affected []string, now time.Time) error {
	unit, err := newLocalEnergyTransaction(ctx, tx, schema)
	if err != nil {
		return err
	}
	loc := unit.reportTZLocation()
	today := now.In(loc).Format(isoDate)
	targets := make(map[string]struct{})
	for _, date := range affected {
		start, err := time.Parse(isoDate, date)
		if err != nil {
			return err
		}
		for offset := 0; offset < energyWindowDays; offset++ {
			target := start.AddDate(0, 0, offset).Format(isoDate)
			if target > today {
				break
			}
			targets[target] = struct{}{}
		}
	}
	dates := slices.Sorted(maps.Keys(targets))
	if len(dates) == 0 {
		return nil
	}

	from, to := subtractDays(dates[0], energyWindowDays-1), dates[len(dates)-1]
	inputs, err := unit.loadLocalShadowEnergyInputs(ctx, profile, from, to)
	if err != nil {
		return err
	}

	cfg := unit.GetEnergyConfig()
	for _, date := range dates {
		res, _, err := unit.readEnergyBankSQL(ctx, date, cfg, inputs)
		if err != nil {
			return err
		}
		if date == today {
			err = unit.publishLocalShadowEnergyToday(ctx, tx, schema, profile, today, loc, now, res)
		} else {
			err = unit.writeHistoricalEnergyResult(ctx, date, loc, res)
		}
		if err != nil {
			return fmt.Errorf("refresh local shadow EnergyBank for %s: %w", date, err)
		}
	}
	return nil
}

func (s *DB) publishLocalShadowEnergyToday(ctx context.Context, tx pgx.Tx, schema, profile, today string, loc *time.Location, now time.Time, res BankResult) error {
	generations, err := localEnergySleepGenerations(ctx, tx, profile, today)
	if err != nil {
		return err
	}
	if err := writeLocalShadowCurrentEnergy(ctx, tx, schema, profile, today, now, res, generations); err != nil {
		return err
	}
	return s.writeEnergySnapshotAt(ctx, loc, now, res, false)
}

// ReconcileLocalShadowEnergy runs under the sleep profile lock on startup.
// The current projection is updated when its published sleep window changed
// while EnergyBank was disabled. Startup does not record an observation.
func ReconcileLocalShadowEnergy(ctx context.Context, tx pgx.Tx, schema, profile string, now time.Time) error {
	unit, err := newLocalEnergyTransaction(ctx, tx, schema)
	if err != nil {
		return err
	}
	today := now.In(unit.reportTZLocation()).Format(isoDate)
	generations, err := localEnergySleepGenerations(ctx, tx, profile, today)
	if err != nil {
		return err
	}
	var unchanged bool
	err = tx.QueryRow(ctx, `SELECT COALESCE((
			SELECT sleep_generations IS NOT DISTINCT FROM $4::jsonb
			FROM sleep_shadow.energy_current WHERE profile=$1 AND tenant_schema=$2 AND local_date=$3::text::date
		), $4::jsonb='{}'::jsonb)`, profile, schema, today, generations).Scan(&unchanged)
	if err != nil || unchanged {
		return err
	}

	inputs, err := unit.loadLocalShadowEnergyInputs(ctx, profile, subtractDays(today, energyWindowDays-1), today)
	if err != nil {
		return err
	}
	res, _, err := unit.readEnergyBankSQL(ctx, today, unit.GetEnergyConfig(), inputs)
	if err != nil {
		return err
	}
	return writeLocalShadowCurrentEnergy(ctx, tx, schema, profile, today, now, res, generations)
}

func newLocalEnergyTransaction(ctx context.Context, tx pgx.Tx, schema string) (*DB, error) {
	if _, err := tx.Exec(ctx, "SELECT set_config('search_path',$1,true)", pgx.Identifier{schema}.Sanitize()); err != nil {
		return nil, err
	}
	return &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}, nil
}

func localEnergySleepGenerations(ctx context.Context, tx pgx.Tx, profile, today string) (json.RawMessage, error) {
	var generations json.RawMessage
	err := tx.QueryRow(ctx, `SELECT COALESCE(jsonb_object_agg(wake_date::text,generation),'{}'::jsonb)
		FROM sleep_shadow.recalculation WHERE profile=$1 AND wake_date BETWEEN $2::text::date AND $3::text::date`,
		profile, subtractDays(today, energyWindowDays-1), today).Scan(&generations)
	return generations, err
}

func (s *DB) loadLocalShadowEnergyInputs(ctx context.Context, profile, from, to string) (map[string]dailyInputs, error) {
	inputs, err := s.loadDailyEnergyInputs(ctx, from, to)
	if err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT r.wake_date::text,d.sleep_seconds/3600,d.deep_seconds/3600,
		       d.rem_seconds/3600,d.awake_seconds/3600
		FROM sleep_shadow.recalculation r
		LEFT JOIN sleep_shadow.daily d USING (profile,wake_date)
		WHERE r.profile=$1 AND r.wake_date BETWEEN $2::text::date AND $3::text::date`, profile, from, to)
	return applySleepEnergyRows(inputs, rows, err)
}

func applySleepEnergyRows(inputs map[string]dailyInputs, rows pgx.Rows, err error) (map[string]dailyInputs, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var date string
		var total, deep, rem, awake *float64
		if err := rows.Scan(&date, &total, &deep, &rem, &awake); err != nil {
			return nil, err
		}
		// A published empty date clears all four fields. Only unpublished
		// days retain the legacy sleep block.
		input := inputs[date]
		input.SleepTotal, input.SleepDeep, input.SleepRem, input.SleepAwake = total, deep, rem, awake
		inputs[date] = input
	}
	return inputs, rows.Err()
}

func writeLocalShadowCurrentEnergy(ctx context.Context, tx pgx.Tx, schema, profile, date string, now time.Time, res BankResult, generations json.RawMessage) error {
	flags := res.Flags
	if flags == nil {
		flags = []string{}
	}
	_, err := tx.Exec(ctx, `INSERT INTO sleep_shadow.energy_current
		(profile,tenant_schema,local_date,state,bank,drain_delta,restore_delta,formula_version,flags,calculated_at,sleep_generations)
		VALUES ($1,$2,$3::text::date,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)
		ON CONFLICT (profile,tenant_schema) DO UPDATE SET
			local_date=EXCLUDED.local_date,state=EXCLUDED.state,bank=EXCLUDED.bank,
			drain_delta=EXCLUDED.drain_delta,restore_delta=EXCLUDED.restore_delta,
			formula_version=EXCLUDED.formula_version,flags=EXCLUDED.flags,calculated_at=EXCLUDED.calculated_at,
			sleep_generations=EXCLUDED.sleep_generations`,
		profile, schema, date, res.State, res.Bank, res.TodayDrain, res.TodayRestore, res.FormulaVersion, flags, now, generations)
	return err
}
