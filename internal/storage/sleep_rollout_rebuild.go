package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// RebuildSleepRolloutPreparation repairs already owned dates before a switch.
// Each date commits independently. Retrying the same range skips matching
// results and cannot claim a night on an unowned date.
func (s *DB) RebuildSleepRolloutPreparation(ctx context.Context, tenant uuid.UUID, from, through string) (int, error) {
	first, err := time.Parse(time.DateOnly, from)
	if err != nil {
		return 0, err
	}
	last, err := time.Parse(time.DateOnly, through)
	if err != nil {
		return 0, err
	}
	if last.Before(first) || tenant == uuid.Nil || !s.sleepRolloutTenant {
		return 0, errors.New("rebuild requires a restricted tenant and an increasing date range")
	}
	window := sleepRolloutWindow{From: from, Through: through}
	if err := s.configureSleepRolloutPreparation(ctx, tenant, window); err != nil {
		return 0, err
	}
	var repaired int
	for day := first; !day.After(last); day = day.AddDate(0, 0, 1) {
		date := day.Format(time.DateOnly)
		changed, err := s.rebuildSleepRolloutPreparationDate(ctx, tenant, date, window)
		if err != nil {
			return repaired, fmt.Errorf("rebuild prepared sleep %s: %w", date, err)
		}
		if changed {
			repaired++
		}
	}
	return repaired, nil
}

func (s *DB) configureSleepRolloutPreparation(ctx context.Context, tenant uuid.UUID, window sleepRolloutWindow) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	if err := lockSleepRollout(ctx, tx); err != nil {
		return err
	}
	if err := verifySleepRolloutTenant(ctx, tx, tenant); err != nil {
		return err
	}
	if err := requireSleepRolloutPreparation(ctx, tx); err != nil {
		return err
	}
	raw, err := json.Marshal(window)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO settings(key,value) VALUES ($1,$2)
		ON CONFLICT (key) DO UPDATE SET value=excluded.value,updated_at=NOW()::text
		WHERE settings.value IS DISTINCT FROM excluded.value`, sleepRolloutPreparationWindowSetting, string(raw)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func requireSleepRolloutPreparation(ctx context.Context, tx pgx.Tx) error {
	var mode string
	if err := tx.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1 FOR SHARE`, SleepRolloutModeSetting).Scan(&mode); err != nil {
		return err
	}
	if mode != "prepare" {
		return ErrSleepRolloutDisabled
	}
	return nil
}

func (s *DB) rebuildSleepRolloutPreparationDate(ctx context.Context, tenant uuid.UUID, date string, window sleepRolloutWindow) (bool, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return false, err
	}
	defer tx.Rollback(context.Background())
	if err := verifySleepRolloutTenant(ctx, tx, tenant); err != nil {
		return false, err
	}
	if err := lockSleepRollout(ctx, tx); err != nil {
		return false, err
	}
	if err := requireSleepRolloutPreparation(ctx, tx); err != nil {
		return false, err
	}
	var latest int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(sequence),0) FROM sleep_rollout_packets WHERE applied_at IS NOT NULL`).Scan(&latest); err != nil {
		return false, err
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	configured, err := unit.sleepRolloutPreparationWindow(ctx)
	if err != nil {
		return false, err
	}
	if configured != window {
		return false, ErrSleepRolloutEvidenceChanged
	}
	if date > time.Now().In(unit.reportTZLocation()).Format(time.DateOnly) {
		return false, errors.New("cannot prepare a future local date")
	}
	cfg := unit.GetEnergyConfig()
	prior, err := unit.auditSleepRolloutDay(ctx, date, cfg, window)
	if err != nil {
		return false, err
	}
	if prior.Sleep == "unowned" || prior.Result == "match" {
		return false, nil
	}
	if err := unit.prepareSleepRolloutResult(ctx, date, latest, cfg, window); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}
