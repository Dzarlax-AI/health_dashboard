package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"log"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

// SustainedHRLoadResult is the §4.4 stress-drain output for one date —
// computed by orchestrating WakeTimeForDate + HRCoverageHours +
// PersonalBaseline(hr_awake) + HourlyHRSeriesForAwakeWindow and feeding
// the resulting per-hour z-series through the shared briefing stress SQL.
//
//   - SustainedHRLoadZ: dimensionless z-load that feeds the
//     β · sustained_hr_load term in DrainV2. Higher = more autonomic
//     load. Returns 0 when any prerequisite fails (coverage gate,
//     calibration state, missing baseline).
//
//   - HROvershootBpmHours: raw bpm·hours sum — Σ max(0, hour_hr[h] −
//     baseline_hr_awake) over the awake window. Stored alongside the
//     z-load in components JSONB so users can hear "HR ran ~8 bpm
//     above your normal for ~4 hours" without back-converting z-units.
//
//   - Flags: subset of {stale_stress, data_accruing, calibration_warmup,
//     acute_stress, sustained_load, illness_signature, recovery_debt,
//     parasympathetic_rebound} — §4.3 stratified flags surfaced to
//     PR-9's verdict layer. Empty when everything is steady.
//     stale_stress fires only when the day ended with coverage < 8h;
//     intraday gating uses data_accruing instead so the UI can carry
//     the "still gathering" connotation rather than "sensor broken".
//
// Returns ok=false on hard DB error or unparseable date — caller writes
// NULL to daily_scores.sustained_hr_load. ok=true with Z=0 happens when
// a gate (coverage, calibration) fires — the day is "honestly zero",
// not "errored".
type SustainedHRLoadResult struct {
	signalInputs        *stressSignalInputs
	SustainedHRLoadZ    float64
	HROvershootBpmHours float64
	Flags               []string
}

// A receipt preserves the exact ordered awake-window observations, including
// non-finite gaps that interrupt a sustained run.
type stressSignalInputs struct {
	series                       []HourlyHRStat
	baseline, baselineSD         float64
	hourZ                        []float64
	threshold                    float64
	temp, resp, drop, rhr, dayHR *float64
}

// ComputeSustainedHRLoadForDate is the §4.4 orchestrator. Pure read
// against existing helpers; no writes. Wraps `WakeTimeForDate +
// HRCoverageHours + PersonalBaseline + HourlyHRSeriesForAwakeWindow`
// and converts per-hour HR into z-load via the shared hourly stress SQL and
// the shared dbt stress/load predicates.
//
// Each prerequisite returns (zero, flag, ok=true) on degraded data
// rather than erroring — the formula's response to partial data should
// match its response to fully-missing data (the §4.3 stale_stress
// flag), not a crash.
//
// `loc` must be the tenant's REPORT_TZ.
func (s *DB) ComputeSustainedHRLoadForDate(
	date string,
	loc *time.Location,
) (SustainedHRLoadResult, bool) {
	ctx, cancel := longCtx()
	defer cancel()
	result, ok, _ := s.ComputeSustainedHRLoadForDateContext(ctx, date, loc)
	return result, ok
}

// ComputeSustainedHRLoadForDateContext is the fail-closed form used by
// historical maintenance. Insufficient physiological data remains a valid
// gated result; database and cancellation errors are returned.
func (s *DB) ComputeSustainedHRLoadForDateContext(
	ctx context.Context,
	date string,
	loc *time.Location,
) (SustainedHRLoadResult, bool, error) {
	if loc == nil {
		loc = time.UTC
	}
	if _, err := time.ParseInLocation("2006-01-02", date, loc); err != nil {
		return SustainedHRLoadResult{}, false, err
	}

	res := SustainedHRLoadResult{Flags: []string{}}

	// Coverage gate (§0 blocker 3 / §4.4 MIN_COVERAGE = 8 hours).
	// Below threshold, the per-hour HR samples don't cover enough of
	// the awake window to compute a trustworthy sustained-load
	// integral. Flag and short-circuit before the heavier baseline
	// query.
	// HR coverage / baseline / series gates short-circuit the
	// sustained_hr_load integral path. The §4.3 multi-channel flags
	// (illness_signature, recovery_debt, parasympathetic_rebound)
	// MUST still run on those days — temp/resp/HRV/overnight-RHR
	// channels have their own §4.1 calibration state machines and
	// shouldn't be silenced by an HR-only gate. Stash the gated flag
	// and fall through to the shared multi-channel call below.
	gated := ""
	coverage, ok, err := s.HRCoverageHoursContext(ctx, date, loc)
	if err != nil {
		return SustainedHRLoadResult{}, false, err
	}
	if !ok {
		// Hard DB error — log already done by HRCoverageHours.
		// Treat as "no data" rather than failing the orchestrator.
		gated = "stale_stress"
	}
	if gated == "" && coverage < health.MinHRCoverageHours {
		// Distinguish the in-progress case (day still has hours to
		// accumulate HR) from the post-hoc case (day ended with a real
		// coverage gap — watch off, sync break). Same gate behaviour
		// either way (sustained-load drain falls back to v2.0 kcal),
		// but the verdict layer and UI carry very different
		// connotations: "still gathering" vs "sensor problem".
		gated = "stale_stress"
		if _, awakeEnd, okBounds, boundsErr := s.AwakeWindowBoundsContext(ctx, date, loc); boundsErr != nil {
			return SustainedHRLoadResult{}, false, boundsErr
		} else if okBounds && time.Now().In(loc).Before(awakeEnd) {
			gated = "data_accruing"
		}
	}

	// Personal baseline (§4.1). Cold state (<3 samples) gates the
	// channel; warmup state passes through with a flag so PR-9
	// verdict layer softens the narrative.
	var bl PersonalBaselineResult
	if gated == "" {
		var blOK bool
		bl, blOK, err = s.PersonalBaselineContext(ctx, date, ChannelHRAwake, 30, loc)
		if err != nil {
			return SustainedHRLoadResult{}, false, err
		}
		if !blOK {
			gated = "stale_stress"
		} else if bl.State == CalibrationWarmup {
			res.Flags = append(res.Flags, "calibration_warmup")
		}
	}

	// Per-hour HR series over the awake window. WakeTimeForDate is
	// invoked inside; imputed window → still works against fixed
	// 07:00-22:00 fallback.
	var series []HourlyHRStat
	if gated == "" {
		series, ok, err = s.HourlyHRSeriesForAwakeWindowContext(ctx, date, loc)
		if err != nil {
			return SustainedHRLoadResult{}, false, err
		}
		if !ok || len(series) == 0 {
			gated = "stale_stress"
		}
	}

	if gated != "" {
		res.Flags = append(res.Flags, gated)
		// Multi-channel flags still apply — they read independent
		// autonomic channels. Pass nil hourZ; meanFiniteHourZ
		// returns ok=false on empty so parasympathetic_rebound is
		// naturally skipped, while illness_signature / recovery_debt
		// can still fire from temp/resp/HRV/overnight-RHR.
		err = s.applyStressSignals(ctx, date, loc, nil, 0, &res)
		if err != nil {
			return SustainedHRLoadResult{}, false, err
		}
		return res, true, nil
	}

	heartRates := make([]float64, len(series))
	covered := make([]bool, len(series))
	for i, h := range series {
		heartRates[i], covered[i] = h.Median5minMinHR, h.CoverageOK
	}
	var hourZ []float64
	err = s.pool.QueryRow(ctx, hourlyStressSeriesSQL, heartRates, covered, bl.Median, bl.MADSD).Scan(&hourZ, &res.HROvershootBpmHours)
	if err != nil {
		return SustainedHRLoadResult{}, false, err
	}

	// Read the live tenant config so the z-threshold is settings-
	// driven (PR-7's energy.z_threshold, default 0.5 per §4.4).
	threshold, err := s.energyZThresholdContext(ctx)
	if err != nil {
		return SustainedHRLoadResult{}, false, err
	}
	err = s.applyStressSignals(ctx, date, loc, hourZ, threshold, &res)
	if err != nil {
		return SustainedHRLoadResult{}, false, err
	}

	res.signalInputs.series = series
	res.signalInputs.baseline = bl.Median
	res.signalInputs.baselineSD = bl.MADSD

	return res, true, nil
}

// Preserve gaps in the ordered signal series expected by the daily load calculation.
var hourlyStressSeriesSQL = `with hours as (
    select observation.*, $3::float8 as baseline,$4::float8 as sd
    from unnest($1::float8[], $2::bool[]) with ordinality observation(hour_hr,coverage_ok,position)
), scored as (` + dbtmetrics.HourlyStressSQL + `)
select array_agg(coalesce(hour_z,'NaN'::float8) order by position),
       coalesce(sum(overshoot order by position),0) from scored`

func (s *DB) energyZThresholdContext(ctx context.Context) (float64, error) {
	var value sql.NullString
	err := s.pool.QueryRow(ctx, `SELECT value FROM settings WHERE key = 'energy.z_threshold'`).Scan(&value)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if value.Valid && value.String != "" {
		if parsed, parseErr := strconv.ParseFloat(value.String, 64); parseErr == nil {
			return parsed, nil
		}
	}
	return DefaultEnergyConfig().ZThreshold, nil
}

// upsertSustainedHRLoadForDate computes the v2.2 sustained-load value
// AND the §4.3 HR-driven stress flags and writes both to
// daily_scores. NULL/empty when the orchestrator returns ok=false;
// the conditional UPDATE never overwrites a valid prior value with
// NULL.
//
// The stress_flags column is updated together with sustained_hr_load
// because both come from the same orchestrator run — splitting them
// would require duplicate compute work or stale flags during the
// brief window between two UPDATEs. `flags` always written even when
// the load itself is gated to zero (e.g. stale_stress flag fires
// AND empty load → ([stale_stress], 0)).
// upsertSustainedHRLoadForDate returns the computed result and a
// write error (if any). The internal callers (aggregates.go,
// buildSustainedHRLoadAll) discard the return values — they already
// log-and-continue on individual-date failures. The exported wrapper
// surfaces both so cmd/energy_backfill --include-stress can count
// write errors into its progress tally instead of silently treating
// a failed UPDATE as success.
//
// Returns ok=false from ComputeSustainedHRLoadForDate → returns
// (zero-value, nil): no compute → no write → no error. Compute
// succeeded but write failed → returns (res, err).
func (s *DB) upsertSustainedHRLoadForDate(date string, loc *time.Location) (SustainedHRLoadResult, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	return s.upsertSustainedHRLoadForDateContext(ctx, date, loc)
}

// upsertSustainedHRLoadForDateContext is the bounded-maintenance writer. The
// existing calculation helpers retain their established semantics; the
// maintenance context bounds the durable write and is checked around compute.
func (s *DB) upsertSustainedHRLoadForDateContext(ctx context.Context, date string, loc *time.Location) (SustainedHRLoadResult, error) {
	if err := ctx.Err(); err != nil {
		return SustainedHRLoadResult{}, err
	}
	res, ok, err := s.ComputeSustainedHRLoadForDateContext(ctx, date, loc)
	if err != nil {
		return SustainedHRLoadResult{}, err
	}
	if !ok {
		return SustainedHRLoadResult{}, fmt.Errorf("compute sustained HR load for %s failed", date)
	}
	if err := ctx.Err(); err != nil {
		return res, err
	}
	return res, s.writeSustainedHRLoadRowContext(ctx, date, res)
}

// writeSustainedHRLoadRow is the UPDATE half of
// upsertSustainedHRLoadForDate, factored out so cmd/energy_backfill
// --include-stress can drive compute + write separately and count
// each kind of failure into its tally without a double-compute.
//
// Always called with a non-nil res; callers handle the "no compute"
// path themselves.
func (s *DB) writeSustainedHRLoadRow(date string, res SustainedHRLoadResult) error {
	ctx, cancel := queryCtx()
	defer cancel()
	return s.writeSustainedHRLoadRowContext(ctx, date, res)
}

func (s *DB) writeSustainedHRLoadRowContext(ctx context.Context, date string, res SustainedHRLoadResult) error {
	flags := res.Flags
	if flags == nil {
		flags = []string{}
	}
	// Sentinel for the SustainedHRLoadZ scalar — only update when
	// finite. Flags update independently of load validity.
	var load *float64
	if isFiniteFloat(res.SustainedHRLoadZ) {
		v := res.SustainedHRLoadZ
		load = &v
	}
	const update = `UPDATE daily_scores SET sustained_hr_load=coalesce($2,sustained_hr_load),
		stress_flags=$3 where date=$1`
	args := []any{date, load, flags}
	write := func(db *DB, capture bool) (bool, error) {
		query := update
		params := args
		if capture && res.signalInputs != nil {
			i := res.signalInputs
			query = `with saved as (` + update + ` returning date), decision_packets as (
				select 'stress_signals'::text metric,date decision_key,1 formula_version,
				jsonb_build_object('hour_z',$4::float8[],'z_threshold',$5::float8,
				 'temp',$6::float8,'resp',$7::float8,'drop',$8::float8,'rhr',$9::float8,'day_hr',$10::float8) || case when $11::timestamptz[] is null then '{}'::jsonb
                    else jsonb_build_object('stress_hours',(select jsonb_agg(jsonb_build_object(
                        'hour_start',h.start_at,'hour_hr',h.hr,'coverage_ok',h.covered,
                        'baseline',$14::float8,'sd',$15::float8) order by h.ord)
                        from unnest($11::timestamptz[],$12::float8[],$13::boolean[]) with ordinality
                        h(start_at,hr,covered,ord))) end inputs,
				jsonb_build_object('load',$2::float8,'flags',$3::text[]) result from saved
			)` + decisionJournalInsertSQL + " select count(*) from saved"
			var starts []time.Time
			var heartRates []float64
			var covered []bool
			for _, h := range i.series {
				starts = append(starts, h.Hour.UTC())
				heartRates = append(heartRates, h.Median5minMinHR)
				covered = append(covered, h.CoverageOK)
			}
			params = append(args, i.hourZ, i.threshold, i.temp, i.resp, i.drop, i.rhr, i.dayHR, starts, heartRates, covered, i.baseline, i.baselineSD)
		}
		_, err := db.pool.Exec(ctx, query, params...)
		return false, err
	}
	var err error
	if res.signalInputs == nil || len(res.signalInputs.hourZ) > 25 {
		_, err = write(s, false)
	} else {
		_, err = withDecisionJournal(ctx, s, write)
	}
	if err != nil {
		log.Printf("writeSustainedHRLoadRow %s: %v", date, err)
		return err
	}
	return nil
}

// UpsertSustainedHRLoadForDate is the exported wrapper, mirroring the
// PR-2 UpsertBaselineHROvernightForDate pattern. Returns the computed
// result and any write error so backfill callers can distinguish
// compute-failed (ok=false-style) from write-failed cases.
func (s *DB) UpsertSustainedHRLoadForDate(date string, loc *time.Location) (SustainedHRLoadResult, error) {
	return s.upsertSustainedHRLoadForDate(date, loc)
}

// buildSustainedHRLoadAll backfills daily_scores.sustained_hr_load for
// every existing date. Called from BackfillAggregates AFTER the sleep
// block + baseline_hr_overnight pass so the orchestrator's per-channel
// PersonalBaseline reads see the freshest data.
//
// Logs+continues on individual-date errors — same pattern as
// buildBaselineHROvernightAll. One bad night shouldn't fail a
// months-long backfill.
func (s *DB) buildSustainedHRLoadAll(force bool) {
	_ = force
	ctx, cancel := longCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT date FROM daily_scores ORDER BY date`)
	if err != nil {
		log.Printf("sustained_hr_load list: %v", err)
		return
	}
	var dates []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			continue
		}
		dates = append(dates, d)
	}
	if err := rows.Err(); err != nil {
		// Cursor / transport failure mid-iteration. Partial dates
		// slice would silently produce a misleading
		// "filled for N dates" log without the whole history
		// touched. Bail with the error logged instead.
		rows.Close()
		log.Printf("sustained_hr_load iter: %v", err)
		return
	}
	rows.Close()
	if len(dates) == 0 {
		return
	}
	loc := s.reportTZLocation()
	for _, d := range dates {
		// log-and-continue: individual-date failures are already
		// logged inside; the backfill summary line below is "filled
		// for N dates" regardless. cmd/energy_backfill --include-
		// stress is the path that wants per-date error counts.
		_, _ = s.upsertSustainedHRLoadForDate(d, loc)
	}
	log.Printf("sustained_hr_load filled for %d dates", len(dates))
}
