package storage

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
)

//go:embed sql/hdbt_v17.sql
var hdbtDDL string

const hdbtFormula = "aggregate_serving_v1"

func markAggregateDatesDirty(ctx context.Context, tx pgx.Tx, state *CacheMaintenanceState, dates []string) error {
	if len(dates) == 0 {
		return nil
	}
	if err := markCacheDirtyState(state, dates); err != nil {
		return err
	}
	// The source mutation already holds the Go settings row lock. Enrollment
	// and publication observe the same generation before taking their state lock.
	var installed bool
	if err := tx.QueryRow(ctx, "select to_regclass(format('%I.hdbt_state',current_schema())) is not null").Scan(&installed); err != nil {
		return err
	}
	if !installed {
		return nil
	}
	var enabled bool
	if err := tx.QueryRow(ctx, "select shadow_enabled from hdbt_state where singleton for share").Scan(&enabled); err != nil {
		return fmt.Errorf("read aggregate enrollment: %w", err)
	}
	if !enabled {
		return nil
	}
	generation := strconv.FormatUint(state.Generation, 10)
	_, err := tx.Exec(ctx, `insert into hdbt_dirty(date,generation)
        select day,$2::text::numeric from unnest($1::text[]) day order by day
        on conflict(date) do update set generation=excluded.generation`, dates, generation)
	if err != nil {
		return err
	}
	saved, err := tx.Exec(ctx, `insert into hdbt_step_repairs(point_date,source,generation)
        select distinct cell->>0,cell->>1,$1::text::numeric
        from jsonb_array_elements(coalesce(nullif(current_setting('health.hdbt_step_cells',true),''),'[]')::jsonb) cell
        order by 1,2 on conflict(point_date,source) do update set generation=excluded.generation`, generation)
	if err != nil || saved.RowsAffected() == 0 {
		return err
	}
	_, err = tx.Exec(ctx, "select set_config('health.hdbt_step_cells','',true)")
	return err
}

// An optional publication can fail independently (including missing grants or
// a relation during rollback). A savepoint leaves the Go fallback readable in
// the same repeatable-read snapshot.
func readHDBTSteps(ctx context.Context, tx pgx.Tx, from, to string, generation uint64) ([]DailyStepPoint, bool) {
	var points []DailyStepPoint
	selected := false
	err := pgx.BeginFunc(ctx, tx, func(unit pgx.Tx) error {
		var eligible bool
		err := unit.QueryRow(ctx, `select shadow_enabled and read_enabled and ready and initialized
            and formula_id=$1 and coalesce(source_generation=$2::text::numeric,false)
            and to_regclass(format('%I.hdbt_dirty',current_schema()))='hdbt_dirty'::regclass
            and to_regclass(format('%I.hdbt_daily_metrics',current_schema()))='hdbt_daily_metrics'::regclass
            and not exists(select from hdbt_dirty)
            from hdbt_state where singleton and tableoid=to_regclass(format('%I.hdbt_state',current_schema()))`, hdbtFormula, strconv.FormatUint(generation, 10)).Scan(&eligible)
		if err != nil || !eligible {
			return err
		}
		rows, err := unit.Query(ctx, `select date,steps::float8 as qty from hdbt_daily_metrics
            where date between $1 and $2 and steps is not null order by date`, from, to)
		if err != nil {
			return err
		}
		points, err = pgx.CollectRows(rows, pgx.RowToStructByPos[DailyStepPoint])
		selected = err == nil
		return err
	})
	return points, err == nil && selected
}
