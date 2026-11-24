package storage

import (
	"context"
	"errors"
	"strconv"

	dbtmetrics "health-receiver/experiments/dbt_metrics"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var errHDBTStepEvidenceUnavailable = errors.New("dbt step withdrawal evidence unavailable")

func hdbtDirtyStepDates(ctx context.Context, tx pgx.Tx, from, to string) ([]string, error) {
	var days []string
	err := tx.QueryRow(ctx, `select array(select distinct to_char(d.date::date+n,'YYYY-MM-DD') from hdbt_dirty d
		cross join generate_series(-2,2) n where (d.date::date+n) between
		coalesce(nullif($1,'')::date,date '0001-01-01') and $2::text::date)`, from, to).Scan(&days)
	return days, err
}

// readHDBTSteps serves the daily chart for both bounded and all-time reads. A disabled enrollment or read flag
// returns control to the ordinary v6 reader. An enrolled stale publication
// uses chronological minute sums, including when the Go dirty queue was ACKed.
func readHDBTSteps(ctx context.Context, tx pgx.Tx, from, to string, generation *uint64) ([]DailyStepPoint, bool, error) {
	if from == "" {
		from = "0001-01-01"
	}
	var enabled, compatible, ready bool
	var watermark *string
	var points []DailyStepPoint
	var complete bool
	err := pgx.BeginFunc(ctx, tx, func(unit pgx.Tx) error {
		if err := unit.QueryRow(ctx, `select shadow_enabled and read_enabled,
             initialized and formula_id=$1,ready,source_generation::text
             from hdbt_state where singleton and tableoid=to_regclass(format('%I.hdbt_state',current_schema()))`, hdbtFormula).
			Scan(&enabled, &compatible, &ready, &watermark); err != nil {
			return err
		}
		if !enabled || !compatible || watermark == nil || generation == nil {
			return nil
		}
		published, err := strconv.ParseUint(*watermark, 10, 64)
		if err != nil || published > *generation {
			return nil
		}

		// Bootstrap and formula changes have no certified prior series. When
		// updates span several batches, each retired day is published in the
		// same transaction as its ACK. Remaining dirty dates are independent
		// of the ordinary Go journal and include cross-date alias neighbors.
		var bootstrapping, hasDirty bool
		if err := unit.QueryRow(ctx, `select exists(select from hdbt_backfill),exists(select from hdbt_dirty)`).
			Scan(&bootstrapping, &hasDirty); err != nil {
			return err
		}
		if bootstrapping || (!hasDirty && (!ready || published != *generation)) {
			return nil
		}
		query := "select date,steps::float8 as qty from hdbt_daily_metrics where date between $1 and $2 and steps is not null"
		args := []any{from, to}
		if hasDirty {
			dirty, err := hdbtDirtyStepDates(ctx, unit, from, to)
			if err != nil {
				return err
			}
			if len(dirty) > 0 {
				query += " and date<>all($3::text[]) union all select * from (" + currentHDBTStepsSQL(true) + ") changed"
				args = append(args, dirty)
			}
		}
		points, err = collectHDBTSteps(ctx, unit, query+" order by date", args...)
		complete = err == nil
		return err
	})
	if !enabled {
		return nil, false, nil
	}
	if err == nil && complete {
		return points, true, nil
	}
	// Use retained withdrawal evidence when it is available. If it cannot be
	// read, let the ordinary reader certify its own cache against the Go journal.
	err = pgx.BeginFunc(ctx, tx, func(unit pgx.Tx) error {
		var readErr error
		points, readErr = collectHDBTSteps(ctx, unit, currentHDBTStepsSQL(false)+" order by date", from, to)
		return readErr
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "42P01" || pgErr.Code == "42501") {
		return nil, false, errHDBTStepEvidenceUnavailable
	}
	return points, true, err
}

func collectHDBTSteps(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]DailyStepPoint, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[DailyStepPoint])
}

func currentHDBTStepsSQL(patch bool) string {
	repairCTEs := ""
	scope := " between $1 and $2"
	prior := "daily_scores"
	if patch {
		scope = " = any($3::text[])"
		prior = "hdbt_daily_metrics"
	}
	points := dbtmetrics.ServingStepPointInputsSQL("metric_points", "health_records", "substring(p.date,1,10)"+scope)
	cached := `select hour,source,avg_val,min_val,max_val,sample_count from hourly_metrics h
		where metric_name='step_count' and substring(hour,1,10)` + scope
	if patch {
		// On published dates the worker starts from its retained source cells.
		// A receipt releases only that source in the affected wall hour.
		previousInputs := dbtmetrics.ServingStepPointInputsSQL("hdbt_step_inputs", "health_records", "substring(p.date,1,10)"+scope)
		repairCTEs = `, previous_rows as (
			select * from hdbt_step_inputs where substring(date,1,10)` + scope + `
		), previous_inputs as (` + previousInputs + `), previous_cells as (
			select h.* from hdbt_step_state h where substring(h.hour,1,10)` + scope + ` and h.avg_val is not null
		), proven_edits as (` + dbtmetrics.ServingProvenStepEditsSQL("previous_inputs", "previous_cells", "previous_rows", "substring(n.date,1,10)"+scope) + `),
		ambiguous_hours as (` + dbtmetrics.AmbiguousStepHoursSQL("canonical_inputs") + `), receipts as (
			select distinct substring(point_date,1,13)||':00' as hour,source from hdbt_step_repairs
			where substring(point_date,1,10)` + scope + `
		), repair_points as (
			select 'step_count'::text as metric_name,p.* from canonical_inputs p join receipts r
				on r.hour=substring(p.date,1,13)||':00' and r.source=p.source
		), repaired as (` + dbtmetrics.ServingStepWallRowsSQL("repair_points") + `)`
		cached += ` and not exists(select from hdbt_daily_metrics d where d.date=substring(h.hour,1,10))
			union all select hour,source,avg_val,min_val,max_val,sample_count from hdbt_step_state h
			where substring(hour,1,10)` + scope + ` and avg_val is not null and not withdrawn
			and exists(select from hdbt_daily_metrics d where d.date=substring(h.hour,1,10))
			and not exists(select from receipts r where (r.hour,r.source)=(h.hour,h.source))
			and not exists(select from proven_edits p where p.hour=h.hour
				and not exists(select from ambiguous_hours a where a.hour=p.hour))
			union all select hour,source,avg_val,min_val,max_val,sample_count from repaired h
			where exists(select from hdbt_daily_metrics d where d.date=substring(h.hour,1,10))`
	}
	return `with canonical_inputs as (` + points + `)` + repairCTEs + `, canonical_cache as (` + cached + `),
        recomputed as (` + dbtmetrics.OrderedStepTotalsSQL("canonical_inputs", "canonical_cache") + `)
        select day as date, source_total::real::float8 as qty from recomputed
        union all
        select prior.date,prior.steps::float8 from ` + prior + ` prior
        where prior.date` + scope + ` and prior.steps is not null
          and not exists(select from recomputed r where r.day=prior.date)
          and not exists(select from hdbt_step_repairs r where substring(r.point_date,1,10)=prior.date)
          and not exists(select from hdbt_step_state h where h.withdrawn and substring(h.hour,1,10)=prior.date)
          and not exists(select from canonical_inputs p where not p.counted and substring(p.date,1,10)=prior.date)`
}
