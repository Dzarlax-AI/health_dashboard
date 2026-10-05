package storage

import (
	"context"
	"fmt"
	"sort"
	"time"

	"health-receiver/internal/health"
)

// RefreshChangedCache keeps large imports from monopolizing the cache gate.
// Ordinary small syncs retain their existing single inline refresh path.
func (s *DB) RefreshChangedCache(ctx context.Context, dates []string) error {
	if len(dates) == 0 {
		return nil
	}
	if len(dates) <= 8 {
		return s.UpsertRecentCache(dates, true)
	}
	sorted := append([]string(nil), dates...)
	sort.Strings(sorted)
	for _, date := range sorted {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.UpsertRecentCache([]string{date}, false); err != nil {
			return err
		}
	}
	return s.RecomputeReadinessSince(sorted[0])
}

// RunCacheMaintenance resumes startup recovery without holding the cache lock
// across the historical range. The caller runs one installation-wide worker.
// finish must synchronously refresh Today and drain durable dirty generations.
func (s *DB) RunCacheMaintenance(ctx context.Context, tz string, finish func([]string) error) error {
	state, err := s.LoadCacheMaintenance(ctx)
	if err != nil {
		return err
	}
	if state.TargetIdentity != CacheMaintenanceIdentity() && (state.CompletedIdentity != CacheMaintenanceIdentity() || state.TargetIdentity != "") {
		from, to, empty, err := s.CacheMaintenanceDateRange(ctx)
		if err != nil {
			return err
		}
		if empty {
			return nil
		}
		state, err = s.BeginCacheMaintenance(ctx, from, to)
		if err != nil {
			return err
		}
	}
	if state.TargetIdentity != "" {
		finishRefresh := s.BeginDashboardRefresh()
		defer finishRefresh()
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return err
	}
	for state.NextDate != "" {
		date := state.NextDate
		if err := s.cacheMu.LockBackground(ctx); err != nil {
			return err
		}
		err = func() error {
			defer s.cacheMu.Unlock()
			// Read protection after acquiring the gate: a foreground mutation
			// may have completed while this background unit was waiting.
			current, err := s.LoadCacheMaintenance(ctx)
			if err != nil {
				return err
			}
			unitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			err = s.runCacheTransaction(unitCtx, func(unit *DB) error {
				switch current.Phase {
				case "aggregates":
					if current.ForegroundDates[date] || s.cacheAppliedGenerations[date] != 0 {
						return nil
					}
					return unit.rebuildHistoricalAggregateDate(unitCtx, date)
				case "baseline":
					return unit.upsertBaselineHROvernightForDateContext(unitCtx, date, loc)
				case "sustained":
					_, err := unit.upsertSustainedHRLoadForDateContext(unitCtx, date, loc)
					return err
				case "recovery", "passive", "acute", "chronic", "derived":
					return unit.rebuildMaintenancePhase(unitCtx, current.Phase, date, tz, current.FromDate, current.EndDate)
				default:
					return fmt.Errorf("unsupported maintenance phase %q", current.Phase)
				}
			})
			if err != nil {
				return fmt.Errorf("maintenance %s %s: %w", current.Phase, date, err)
			}

			next, err := addDay(date)
			if err != nil {
				return err
			}
			state, err = s.AdvanceCacheMaintenance(ctx, next)
			return err
		}()
		if err != nil {
			return err
		}
	}
	state, err = s.LoadCacheMaintenance(ctx)
	if err != nil {
		return err
	}
	if len(state.Dirty) != 0 {
		if finish == nil {
			return fmt.Errorf("cache maintenance needs a dependent-stage callback")
		}
		dates := make([]string, 0, len(state.Dirty))
		for date := range state.Dirty {
			dates = append(dates, date)
		}
		sort.Strings(dates)
		if err := finish(dates); err != nil {
			return err
		}
	}
	if finish != nil {
		if err := finish(nil); err != nil {
			return err
		}
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	state, err = s.LoadCacheMaintenance(ctx)
	if err != nil {
		return err
	}
	if len(state.Dirty) != 0 {
		return fmt.Errorf("cache maintenance received a newer correction before publication")
	}
	if state.TargetIdentity != "" && state.Phase != CacheMaintenancePhaseComplete {
		return fmt.Errorf("cache maintenance phases are incomplete")
	}
	return s.runCacheTransaction(ctx, func(unit *DB) error {
		if err := unit.refreshDashboardSnapshotLocked(ctx); err != nil {
			return err
		}
		if state.TargetIdentity != "" {
			_, err := unit.FinishCacheMaintenance(ctx)
			return err
		}
		return nil
	})
}

func (s *DB) rebuildMaintenanceDependencies(ctx context.Context, date, tz string) error {
	points, err := s.computeReadinessHistoryAt(ctx, 1, date)
	if err != nil {
		return err
	}
	for _, p := range points {
		if p.Date != date {
			continue
		}
		if _, err := s.pool.Exec(ctx, `UPDATE daily_scores SET readiness=$2, score_version=$3 WHERE date=$1`, date, p.Score, ScoreVersion); err != nil {
			return err
		}
	}

	if date >= s.Today() {
		return nil // Only completed historical days receive synthetic EOD rows.
	}
	return s.repairHistoricalEnergyDate(ctx, date, tz)
}

// RepairHistoricalDependencies recomputes the causal suffix after a correction.
// Calendar lookbacks are retained; each date yields to foreground mutations.
func (s *DB) RepairHistoricalDependencies(ctx context.Context, dates []string, tz string, today time.Time) error {
	from, to, ok := readinessRedesignRoutineWindow(dates, today)
	if !ok {
		return nil
	}
	// Chronic's 14-day forward target consumes Recovery's additional three nights.
	fromTime, _ := time.Parse(isoDate, from)
	from = fromTime.AddDate(0, 0, -(health.ChronicLoadForwardWindowDays + 3 - readinessRedesignRoutineLookbackDays)).Format(isoDate)
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return err
	}
	for _, phase := range []string{"baseline", "sustained", "recovery", "passive", "acute", "chronic", "derived"} {
		for date := from; date <= to; {
			if err := s.cacheMu.LockBackground(ctx); err != nil {
				return err
			}
			err = func() error {
				defer s.cacheMu.Unlock()
				unitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
				defer cancel()
				return s.runCacheTransaction(unitCtx, func(unit *DB) error {
					switch phase {
					case "baseline":
						return unit.upsertBaselineHROvernightForDateContext(unitCtx, date, loc)
					case "sustained":
						_, err := unit.upsertSustainedHRLoadForDateContext(unitCtx, date, loc)
						return err
					default:
						return unit.rebuildMaintenancePhase(unitCtx, phase, date, tz, from, to)
					}
				})
			}()
			if err != nil {
				return fmt.Errorf("repair %s %s: %w", phase, date, err)
			}
			date, err = addDay(date)
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func (s *DB) rebuildMaintenancePhase(ctx context.Context, phase, date, tz, observationFrom, observationThrough string) error {
	var err error
	switch phase {
	case "recovery":
		var epochs *sourceEpochRunCache
		epochs, err = s.loadSourceEpochRunCache()
		if err == nil {
			_, err = s.backfillRecoveryStabilitySnapshotsForMaintenance(date, date, observationFrom, observationThrough, epochs)
		}
	case "passive":
		_, err = s.BackfillPassiveEfficiencySnapshots(date, date)
	case "acute":
		_, err = s.BackfillAcuteRiskSnapshots(date, date)
	case "chronic":
		_, err = s.BackfillChronicLoadSnapshots(date, date)
	case "derived":
		err = s.rebuildMaintenanceDependencies(ctx, date, tz)
	default:
		return fmt.Errorf("unsupported dependency phase %q", phase)
	}
	return err
}
