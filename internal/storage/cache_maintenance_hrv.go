package storage

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Per-date settings rows survive a crash and remain readable by older binaries,
// whose strictly decoded cache-maintenance journal is intentionally unchanged.
const hrvRepairKeyPrefix = "cache_maintenance_hrv_repair_v1:"

// markCacheDirtyWithHRVRepairsTx commits the source mutation, ordinary dirty
// generations, and exact HRV repair obligations together.
func (s *DB) markCacheDirtyWithHRVRepairsTx(ctx context.Context, tx pgx.Tx, dirtyDates, hrvRepairDates []string) error {
	state, err := lockCacheMaintenanceRow(ctx, tx)
	if err != nil {
		return err
	}
	return s.recordCacheDirtyWithHRVRepairsTx(ctx, tx, state, dirtyDates, hrvRepairDates)
}

// recordCacheDirtyWithHRVRepairsTx uses the caller's already locked state.
func (s *DB) recordCacheDirtyWithHRVRepairsTx(ctx context.Context, tx pgx.Tx, state CacheMaintenanceState, dirtyDates, hrvRepairDates []string) error {
	dates, err := normalizeDirtyDates(append(append([]string(nil), dirtyDates...), hrvRepairDates...))
	if err != nil {
		return err
	}
	hrvDates, err := normalizeDirtyDates(hrvRepairDates)
	if err != nil {
		return err
	}
	if err := markCacheDirtyState(&state, dates); err != nil {
		return err
	}
	if err := saveCacheMaintenanceRow(ctx, tx, state); err != nil {
		return err
	}
	if len(hrvDates) > 0 {
		if _, err := tx.Exec(ctx, `INSERT INTO settings(key,value,updated_at)
			SELECT $1 || day, $2, NOW()::text FROM unnest($3::text[]) AS dates(day) ORDER BY day
			ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value,updated_at=EXCLUDED.updated_at`,
			hrvRepairKeyPrefix, fmt.Sprint(state.Generation), hrvDates); err != nil {
			return err
		}
		// Invalidate every persisted HRV read path before the source mutation
		// commits. Repair may fail or run after restart; stale readiness must
		// not remain reusable merely because it still has today's version.
		if _, err := tx.Exec(ctx, `DELETE FROM hourly_metrics WHERE metric_name='heart_rate_variability' AND SUBSTRING(hour,1,10)=ANY($1)`, hrvDates); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM minute_metrics WHERE metric_name='heart_rate_variability' AND SUBSTRING(minute,1,10)=ANY($1)`, hrvDates); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE daily_scores SET hrv_avg=NULL, readiness=NULL, score_version=NULL WHERE date=ANY($1)`, hrvDates); err != nil {
			return err
		}
		if err := MarkMetricCatalogHoursChangedTx(ctx, tx); err != nil {
			return err
		}
		// Dashboard requests read the completed snapshot rather than hourly
		// rows. Refresh only for confirmed withdrawals, never normal imports.
		unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
		if err := unit.refreshDashboardSnapshotLocked(ctx); err != nil {
			return err
		}
	}
	return MarkMetricCatalogSourceChangedTx(ctx, tx)
}

// prepareHRVRepair locks a pending marker in the current cache transaction and
// removes only stale HRV values. The existing writer must then rebuild that
// date and acknowledgeHRVRepair must run before the same transaction commits.
func (s *DB) prepareHRVRepair(ctx context.Context, date string) (bool, error) {
	if err := validateCacheDate(date); err != nil {
		return false, err
	}
	var value string
	if err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key=$1 FOR UPDATE`, hrvRepairKeyPrefix+date).Scan(&value); errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM hourly_metrics WHERE metric_name='heart_rate_variability' AND SUBSTRING(hour,1,10)=$1`, date); err != nil {
		return false, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE daily_scores SET hrv_avg=NULL WHERE date=$1`, date); err != nil {
		return false, err
	}
	return true, nil
}

func (s *DB) acknowledgeHRVRepair(ctx context.Context, date string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM settings WHERE key=$1`, hrvRepairKeyPrefix+date)
	return err
}

// rebuildRecentHRVRepair uses the same live writers as ordinary foreground
// refresh. The marker lock fences an overlapping source transaction; its new
// marker survives if the source mutation commits after this transaction.
func (s *DB) rebuildRecentHRVRepair(ctx context.Context, date, dailySQL string) (bool, error) {
	var present bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM settings WHERE key=$1)`, hrvRepairKeyPrefix+date).Scan(&present); err != nil {
		return false, err
	}
	if !present {
		return false, nil
	}
	repaired := false
	err := s.runCacheTransaction(ctx, func(unit *DB) error {
		locked, err := unit.prepareHRVRepair(ctx, date)
		if err != nil || !locked {
			return err
		}
		if err := unit.writeRecentHourlyCatalog(ctx, date); err != nil {
			return err
		}
		if err := unit.writeDailyScores(date, dailySQL); err != nil {
			return err
		}

		if err := unit.acknowledgeHRVRepair(ctx, date); err != nil {
			return err
		}
		repaired = true
		return nil
	})
	return repaired, err
}

// reconcileHRVRepairDirty restores ordinary dirty obligations if an older
// binary drained that journal while leaving a forward-compatible marker.
func (s *DB) reconcileHRVRepairDirty(ctx context.Context) error {
	_, err := s.withCacheMaintenanceTx(ctx, func(tx pgx.Tx, state *CacheMaintenanceState) error {
		rows, err := tx.Query(ctx, `SELECT SUBSTRING(key, $2::integer) FROM settings WHERE LEFT(key, LENGTH($1))=$1 ORDER BY key`,
			hrvRepairKeyPrefix, len(hrvRepairKeyPrefix)+1)
		if err != nil {
			return err
		}
		var missing []string
		for rows.Next() {
			var date string
			if err := rows.Scan(&date); err != nil {
				rows.Close()
				return err
			}
			if err := validateCacheDate(date); err != nil {
				rows.Close()
				return err
			}
			if _, ok := state.Dirty[date]; !ok {
				missing = append(missing, date)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		return markCacheDirtyState(state, missing)
	})
	return err
}

// settleEmptyCacheDirty publishes an empty dashboard and clears only captured
// generations if no hourly data remains. Newer mutations and pending HRV
// markers remain dirty; the journal lock fences concurrent source commits.
func (s *DB) settleEmptyCacheDirty(ctx context.Context, captured map[string]uint64) (bool, error) {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	empty := false
	var cleared []string
	err := s.runCacheTransaction(ctx, func(unit *DB) error {
		tx := unit.pool.(*cacheTransaction)
		state, err := lockCacheMaintenanceRow(ctx, tx)
		if err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM hourly_metrics)`).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		for date, generation := range captured {
			if state.Dirty[date] != generation {
				continue
			}
			var repairPending bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT FROM settings WHERE key=$1)`, hrvRepairKeyPrefix+date).Scan(&repairPending); err != nil {
				return err
			}
			if !repairPending {
				delete(state.Dirty, date)
				cleared = append(cleared, date)
			}
		}
		if err := unit.refreshDashboardSnapshotLocked(ctx); err != nil {
			return err
		}
		if err := saveCacheMaintenanceRow(ctx, tx, state); err != nil {
			return err
		}
		empty = true
		return nil
	})
	if err != nil {
		return false, err
	}
	for _, date := range cleared {
		delete(s.cacheAppliedGenerations, date)
	}
	return empty, nil
}
