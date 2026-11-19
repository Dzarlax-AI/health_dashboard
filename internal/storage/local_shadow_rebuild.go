package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// LocalShadowRebuildToday selects the tenant schema in the rebuild transaction
// and returns the calendar date at the command's reference time.
func LocalShadowRebuildToday(ctx context.Context, tx pgx.Tx, schema string, now time.Time) (string, error) {
	unit, err := newLocalEnergyTransaction(ctx, tx, schema)
	if err != nil {
		return "", err
	}
	return now.In(unit.reportTZLocation()).Format(isoDate), nil
}

// RebuildLocalShadowDerivedDate runs the local serving formulas for exactly
// one date. Readiness results are kept separately from the live read path.
func RebuildLocalShadowDerivedDate(ctx context.Context, tx pgx.Tx, schema, profile, version, date string, now time.Time) error {
	unit, err := newLocalEnergyTransaction(ctx, tx, schema)
	if err != nil {
		return err
	}
	readiness, err := unit.GetLocalShadowReadiness(ctx, profile, date)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(readiness)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO sleep_shadow.rebuilt_readiness (profile,tenant_schema,local_date,rebuild_version,result)
		VALUES ($1,$2,$3::text::date,$4,$5)
		ON CONFLICT (profile,tenant_schema,local_date) DO UPDATE
		SET rebuild_version=EXCLUDED.rebuild_version,result=EXCLUDED.result`,
		profile, schema, date, version, json.RawMessage(payload)); err != nil {
		return err
	}
	loc := unit.reportTZLocation()
	if date >= now.In(loc).Format(isoDate) {
		// Today is published by the terminal transaction after input validation.
		return nil
	}
	inputs, err := unit.loadLocalShadowEnergyInputs(ctx, profile, subtractDays(date, energyWindowDays-1), date)
	if err != nil {
		return err
	}
	result := computeCalendarEnergyBank(inputs, date, unit.GetEnergyConfig())
	ts, err := historicalEnergyBucket(date, loc)
	if err != nil {
		return err
	}
	// A zone change moves the synthetic EOD bucket for the same date.
	if _, err := tx.Exec(ctx, `DELETE FROM energy_snapshots
		WHERE date=$1 AND ts_bucket<>$2 AND 'backfilled'=ANY(flags)`, date, ts); err != nil {
		return err
	}
	return unit.writeHistoricalEnergyBucket(ctx, date, ts, result)
}

// PublishLocalShadowRebuildObservation runs after the caller has fenced the
// profile and its tracked inputs. The observation is insert-only.
func PublishLocalShadowRebuildObservation(ctx context.Context, tx pgx.Tx, schema, profile, today string, now time.Time) error {
	unit, err := newLocalEnergyTransaction(ctx, tx, schema)
	if err != nil {
		return err
	}
	loc := unit.reportTZLocation()
	if today != now.In(loc).Format(isoDate) {
		return errors.New("local date changed before rebuild observation publication")
	}
	inputs, err := unit.loadLocalShadowEnergyInputs(ctx, profile, subtractDays(today, energyWindowDays-1), today)
	if err != nil {
		return err
	}
	result := computeCalendarEnergyBank(inputs, today, unit.GetEnergyConfig())
	return unit.publishLocalShadowEnergyToday(ctx, tx, schema, profile, today, loc, now, result)
}
