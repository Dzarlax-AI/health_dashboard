package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

var ErrReadinessHistoryBusy = errors.New("readiness history is already being calculated")

type rolloutBriefingSleep struct {
	blocks   map[string]*LocalShadowSleep
	baseline []float64
}

func (s *DB) sleepRolloutBriefing(lang string, legacyOnly bool) (*health.BriefingResponse, bool, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	var response *health.BriefingResponse
	// Treat a rolled-back publication as a legacy current calculation. A
	// historical five-minute observation from the pilot is still preserved.
	handled, err := s.withSleepRolloutPublication(ctx, func(unit *DB, publication SleepRolloutPublication) error {
		active := publication.goProjection != nil || (publication.Active && !legacyOnly)
		date, err := unit.latestSleepRolloutDate(ctx, active, publication.goProjection)
		if err != nil {
			return err
		}
		if date == "" {
			response, err = unit.getHealthBriefing(lang, nil, nil)
			return err
		}
		var overlay *rolloutBriefingSleep
		var bank BankResult
		if active {
			blocks, baseline, err := unit.loadReaderSleepWindow(ctx, date, publication)
			if err != nil {
				return err
			}
			if publication.goProjection == nil || len(blocks) != 0 {
				overlay = &rolloutBriefingSleep{blocks: blocks, baseline: baseline}
			}
			if publication.goProjection != nil && !hasProjectedEnergyInput(date, blocks) {
				response, err = unit.getHealthBriefing(lang, nil, overlay)
				return err
			}
			bank, err = unit.computeServedSleepBank(ctx, date, blocks, unit.GetEnergyConfig())
			if err != nil {
				return err
			}
		} else {
			bank, err = unit.ComputeBankForDate(ctx, unit.reportTZLocation().String(), date)
			if err != nil {
				return err
			}
		}
		response, err = unit.getHealthBriefing(lang, rolloutCurrentEnergy(date, bank), overlay)
		return err
	})
	return response, handled, err
}

func (s *DB) latestSleepRolloutDate(ctx context.Context, active bool, projection *sleepGoProjection) (string, error) {
	var date string
	today := time.Now().In(s.reportTZLocation()).Format(time.DateOnly)
	query := `SELECT COALESCE(GREATEST(
		(SELECT MAX(SUBSTRING(hour,1,10)) FROM hourly_metrics WHERE SUBSTRING(hour,1,10)<=$1),
		(SELECT MAX(wake_date)::text FROM sleep_rollout_daily d WHERE $2 AND wake_date<=$1::text::date`
	args := []any{today, active}
	if projection != nil {
		query += " AND d.wake_date BETWEEN $3::text::date AND $4::text::date AND " + ownedSleepGoProjectionSQL
		args = append(args, projection.From, projection.Through)
	}
	err := s.pool.QueryRow(ctx, query+")), '')", args...).Scan(&date)
	return date, err
}

func rolloutCurrentEnergy(date string, bank BankResult) *localShadowCurrentEnergy {
	return &localShadowCurrentEnergy{date: date, state: bank.State, LatestEnergySnapshot: LatestEnergySnapshot{
		Bank: bank.Bank, DrainDelta: bank.TodayDrain, RestoreDelta: bank.TodayRestore, FormulaVersion: bank.FormulaVersion, Flags: bank.Flags,
	}}
}

func (s *DB) computeServedSleepBank(ctx context.Context, date string, blocks map[string]*LocalShadowSleep, cfg EnergyConfig) (BankResult, error) {
	inputs, err := s.loadServedSleepBankInputs(ctx, date, blocks)
	if err != nil {
		return BankResult{}, err
	}
	res, _, err := s.readEnergyBankSQL(ctx, date, cfg, inputs)
	return res, err
}

func (s *DB) loadServedSleepBankInputs(ctx context.Context, date string, blocks map[string]*LocalShadowSleep) (map[string]dailyInputs, error) {
	from := subtractDays(date, energyWindowDays-1)
	inputs, err := s.loadDailyEnergyInputs(ctx, from, date)
	if err != nil {
		return nil, err
	}
	for day, block := range blocks {
		if day < from || day > date {
			continue
		}
		input := inputs[day]
		input.SleepTotal, input.SleepDeep, input.SleepRem, input.SleepAwake = block.Total, block.Deep, block.REM, block.Awake
		inputs[day] = input
	}
	return inputs, nil
}

func (s *DB) servedSleepSummary(ctx context.Context, publication SleepRolloutPublication, from, to string) ([]SleepNight, error) {
	legacy, err := s.getLegacySleepSummary(from, to)
	if err != nil {
		return nil, err
	}
	query := `SELECT wake_date::text,sleep_seconds/3600,COALESCE(deep_seconds/3600,0),
		COALESCE(rem_seconds/3600,0),COALESCE(core_seconds/3600,0),
		COALESCE(unspecified_seconds/3600,0),COALESCE(awake_seconds/3600,0)
		FROM sleep_rollout_daily d WHERE wake_date BETWEEN $1::text::date AND $2::text::date`
	args := []any{max(from, publication.From), to}
	if projection := publication.goProjection; projection != nil {
		args = []any{max(from, projection.From), min(to, projection.Through), projection.Sources}
		query = strings.Replace(query, "sleep_seconds/3600", "CASE WHEN ("+readySleepGoProjectionSQL+") THEN sleep_seconds/3600 END", 1)
		query += " AND " + ownedSleepGoProjectionSQL
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SleepNight{}
	published := make(map[string]bool)
	for rows.Next() {
		var night SleepNight
		var total *float64
		if err := rows.Scan(&night.Date, &total, &night.Deep, &night.REM, &night.Core, &night.Unspecified, &night.Awake); err != nil {
			return nil, err
		}
		published[night.Date] = true
		if total != nil {
			night.Total = *total
			out = append(out, night)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, night := range legacy {
		if !published[night.Date] && (publication.goProjection != nil || night.Date < publication.From || night.Date > publication.Through) {
			out = append(out, night)
		}
	}
	slices.SortFunc(out, func(a, b SleepNight) int { return strings.Compare(a.Date, b.Date) })
	return out, nil
}

func (s *DB) servedSleepReadinessHistory(ctx context.Context, publication SleepRolloutPublication, outputDays int) ([]health.ReadinessPoint, error) {
	latest, err := s.latestSleepRolloutDate(ctx, true, publication.goProjection)
	if err != nil || latest == "" || outputDays <= 0 {
		return nil, err
	}
	start := subtractDays(latest, outputDays-1)
	points := []health.ReadinessPoint{}
	legacyByDate := make(map[string]health.ReadinessPoint)
	if start < publication.From || publication.goProjection != nil {
		legacy, err := s.computeReadinessHistoryAt(ctx, outputDays, latest)
		if err != nil {
			return nil, err
		}
		fillReadinessBands(legacy)
		for _, point := range legacy {
			legacyByDate[point.Date] = point
			if point.Date >= start && point.Date < publication.From {
				points = append(points, point)
			}
		}
	}
	day, err := time.Parse(time.DateOnly, max(start, publication.From))
	if err != nil {
		return nil, err
	}
	for date := day.Format(time.DateOnly); date <= latest; date = day.Format(time.DateOnly) {
		if projection := publication.goProjection; projection != nil &&
			subtractDays(date, rolloutReadinessBaselineDays) > projection.Through {
			if point, ok := legacyByDate[date]; ok {
				points = append(points, point)
			}
			day = day.AddDate(0, 0, 1)
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		blocks, baseline, err := s.loadReaderSleepWindow(ctx, date, publication)
		if err != nil {
			return nil, err
		}
		if publication.goProjection != nil && len(blocks) == 0 {
			if point, ok := legacyByDate[date]; ok {
				points = append(points, point)
			}
		} else {
			readiness, err := s.readinessWithSleep(ctx, date, blocks, baseline)
			if err != nil {
				return nil, err
			}
			points = append(points, health.ReadinessPoint{Date: date, Score: readiness.Score, Band: readiness.Band})
		}
		day = day.AddDate(0, 0, 1)
	}
	return points, nil
}

func attachRolloutSleepSource(response *health.BriefingResponse, date string, block *LocalShadowSleep) {
	if response.Sleep == nil {
		return
	}
	response.Sleep.LatestTotal, response.Sleep.LatestDate, response.Sleep.Sources = block.Total, "", nil
	if block.Total == nil {
		return
	}
	response.Sleep.LatestDate = date
	var key [2]string
	if block.Candidate != nil && json.Unmarshal([]byte(*block.Candidate), &key) == nil {
		response.Sleep.Sources = []health.SleepSourceSummary{{
			Source: key[0], Total: *block.Total, Deep: zeroIfNil(block.Deep), REM: zeroIfNil(block.REM),
			Core: zeroIfNil(block.Core), Unspecified: zeroIfNil(block.Unspecified), Awake: zeroIfNil(block.Awake),
		}}
	}
}

// recordServedSleepObservation writes a free slot together with its applied
// candidate. A mixed batch's legacy callback can run before the candidate.
func (s *DB) recordServedSleepObservation(ctx context.Context, loc *time.Location, now time.Time, publication SleepRolloutPublication) (BankResult, error) {
	date := now.In(loc).Format(time.DateOnly)
	blocks, _, err := s.loadReaderSleepWindow(ctx, date, publication)
	if err != nil {
		return BankResult{}, err
	}
	if publication.goProjection != nil && !hasProjectedEnergyInput(date, blocks) {
		return BankResult{}, nil
	}
	result, err := s.computeServedSleepBank(ctx, date, blocks, s.GetEnergyConfig())
	if err != nil || result.State == "stale" {
		return result, err
	}
	var pending bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM sleep_rollout_packets p WHERE p.applied_at IS NULL AND p.wake_date <= $1::text::date
		AND (p.health_record_id IS NULL OR EXISTS (
			SELECT 1 FROM health_records h WHERE h.id=COALESCE(p.recovery_health_record_id,p.health_record_id)
			AND h.processing_status IN ('pending','complete')
		))
	)`, date).Scan(&pending); err != nil || pending {
		return result, err
	}
	return result, s.writeEnergySnapshotAt(ctx, loc, now, result, false)
}

// refreshRolloutEnergyObservation records only free slots once a tenant has
// entered serve. The current bank remains a separate calculation and can
// change in a slot whose observed historical value has already been recorded.
func (s *DB) refreshRolloutEnergyObservation(ctx context.Context, tz string, clock func() time.Time) (BankResult, bool, error) {
	if !s.sleepRolloutTenant {
		return BankResult{}, false, nil
	}
	// Candidate writes and mode changes share this fence. In READ COMMITTED
	// the reads after taking it see the preceding worker's committed inputs.
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return BankResult{}, false, err
	}
	defer tx.Rollback(context.Background())
	if err := lockSleepRollout(ctx, tx); err != nil {
		return BankResult{}, false, err
	}
	publication, err := readSleepRolloutPublication(ctx, tx)
	if err != nil {
		return BankResult{}, false, err
	}
	var projection *sleepGoProjection
	if publication != nil && publication.Projection != nil {
		projection = publication.Projection
	} else if s.sleepGoProjectionEnabled() && (publication == nil || !publication.Active || !s.sleepRolloutServeEnabled()) {
		projection, err = loadSleepGoProjection(ctx, tx)
		if err != nil {
			return BankResult{}, true, err
		}
	}
	if publication == nil && projection == nil && !s.sleepRolloutServeEnabled() {
		return BankResult{}, false, nil
	}
	if publication != nil {
		if err := verifySleepRolloutTenant(ctx, tx, publication.Tenant); err != nil {
			return BankResult{}, false, err
		}
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return BankResult{}, true, err
	}
	now := clock()
	date := now.In(loc).Format(time.DateOnly)
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	if publication != nil && publication.Projection == nil && publication.Active && s.sleepRolloutServeEnabled() {
		result, err := unit.recordServedSleepObservation(ctx, loc, now, *publication)
		if err != nil {
			return result, true, err
		}
		return result, true, tx.Commit(ctx)
	}
	if projection != nil {
		blocks, _, err := unit.loadReaderSleepWindow(ctx, date, SleepRolloutPublication{goProjection: projection})
		if err != nil {
			return BankResult{}, true, err
		}
		if hasProjectedEnergyInput(date, blocks) {
			result, err := unit.computeServedSleepBank(ctx, date, blocks, unit.GetEnergyConfig())
			if err != nil {
				return result, true, err
			}
			if result.State != "stale" {
				if err := unit.writeEnergySnapshotAt(ctx, loc, now, result, false); err != nil {
					return result, true, err
				}
			}
			return result, true, tx.Commit(ctx)
		}
		return BankResult{}, false, nil
	}
	result, err := unit.ComputeBankForDate(ctx, tz, date)
	if err != nil {
		return result, true, err
	}
	if result.State != "stale" || publication == nil {
		if err := unit.writeEnergySnapshotAt(ctx, loc, now, result, publication == nil); err != nil {
			return result, true, err
		}
	}
	return result, true, tx.Commit(ctx)
}

// servedSleepEnergyHistory projects synthetic end-of-day history from the
// active inputs. Historical observations always win an occupied slot.
func (s *DB) servedSleepEnergyHistory(ctx context.Context, publication SleepRolloutPublication, tz string, hours int, now time.Time) ([]EnergySnapshotPoint, error) {
	if hours <= 0 {
		hours = 72
	}
	hours = min(hours, 720)
	points, err := s.getObservedEnergyHistory(ctx, tz, hours, now)
	if err != nil {
		return nil, err
	}
	outputLoc, err := time.LoadLocation(tz)
	if err != nil {
		outputLoc = time.UTC
	}
	loc := s.reportTZLocation()
	affected := func(date string) bool { return date >= publication.From }
	if projection := publication.goProjection; projection != nil {
		rows, err := s.pool.Query(ctx, `SELECT wake_date::text FROM sleep_rollout_daily d
			WHERE wake_date BETWEEN $1::text::date AND $2::text::date AND `+ownedSleepGoProjectionSQL,
			projection.From, projection.Through)
		if err != nil {
			return nil, err
		}
		dates, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return nil, err
		}
		affected = func(date string) bool {
			from := subtractDays(date, energyWindowDays-1)
			return slices.ContainsFunc(dates, func(day string) bool { return day >= from && day <= date })
		}
	}
	occupied := make(map[time.Time]bool)
	points = slices.DeleteFunc(points, func(point EnergySnapshotPoint) bool {
		if affected(point.TS.In(loc).Format(time.DateOnly)) && slices.Contains(point.Flags, "backfilled") {
			return true
		}
		occupied[point.TS.UTC()] = true
		return false
	})
	since := now.Add(-time.Duration(hours) * time.Hour)
	today := now.In(loc).Format(time.DateOnly)
	start, err := time.Parse(time.DateOnly, max(publication.From, since.In(loc).Format(time.DateOnly)))
	if err != nil {
		return nil, err
	}
	for day := start; day.Format(time.DateOnly) < today; day = day.AddDate(0, 0, 1) {
		slot := time.Date(day.Year(), day.Month(), day.Day(), 23, 55, 0, 0, loc)
		if slot.Before(since) || occupied[slot.UTC()] {
			continue
		}
		date := day.Format(time.DateOnly)
		if !affected(date) {
			continue
		}
		blocks, _, err := s.loadReaderSleepWindow(ctx, date, publication)
		if err != nil {
			return nil, err
		}
		bank, err := s.computeServedSleepBank(ctx, date, blocks, s.GetEnergyConfig())
		if err != nil {
			return nil, err
		}
		if bank.State == "stale" {
			continue
		}
		var components json.RawMessage
		if bank.Components != nil {
			components, err = json.Marshal(bank.Components)
			if err != nil {
				return nil, err
			}
		}
		points = append(points, EnergySnapshotPoint{TS: slot.In(outputLoc), Bank: bank.Bank, DrainDelta: bank.TodayDrain,
			RestoreDelta: bank.TodayRestore, FormulaVersion: bank.FormulaVersion,
			Flags: append(slices.Clone(bank.Flags), "backfilled"), Components: components})
	}
	slices.SortFunc(points, func(a, b EnergySnapshotPoint) int { return a.TS.Compare(b.TS) })
	return points, nil
}
