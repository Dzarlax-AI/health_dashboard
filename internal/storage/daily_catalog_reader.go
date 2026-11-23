package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// SettingDailyCatalogReads independently enables the certified completed-day
// columns for one tenant. The Steps All rollout has its own switch.
const SettingDailyCatalogReads = "dbt_completed_daily_metrics_v1"

// The original day rows own the calendar, including days with partial history.
// A published NULL is authoritative. Only a missing publication row falls back.
// $2 always carries the selected date, whose live/sample-count path is separate.
const dailyCatalogSource = `(select a.date, a.baseline_hr_overnight,
        case when b.date is null then a.hrv_avg else b.hrv_avg end as hrv_avg,
        case when b.date is null then a.sleep_total else b.sleep_total end as sleep_total,
        case when b.date is null then a.sleep_deep else b.sleep_deep end as sleep_deep,
        case when b.date is null then a.sleep_rem else b.sleep_rem end as sleep_rem,
        case when b.date is null then a.sleep_core else b.sleep_core end as sleep_core,
        case when b.date is null then a.sleep_unspecified else b.sleep_unspecified end as sleep_unspecified,
        case when b.date is null then a.sleep_awake else b.sleep_awake end as sleep_awake,
        case when b.date is null then a.steps else b.steps end as steps,
        case when b.date is null then a.calories else b.calories end as calories,
        case when b.date is null then a.resp_avg else b.resp_avg end as resp_avg,
        case when b.date is null then a.rhr_avg else b.rhr_avg end as rhr_avg,
        case when b.date is null then a.spo2_avg else b.spo2_avg end as spo2_avg,
        case when b.date is null then a.vo2_avg else b.vo2_avg end as vo2_avg,
        case when b.date is null then a.exercise_min else b.exercise_min end as exercise_min
    from daily_scores a
    left join hdbt_daily_metrics b on b.date=a.date and b.date<$2 and exists (
        select from hdbt_state s
        join settings f on f.key='` + SettingDailyCatalogReads + `' and f.value='true'
        join settings j on j.key='` + cacheMaintenanceSettingKey + `'
        where s.singleton and s.shadow_enabled and s.initialized and s.ready
          and s.formula_id='` + hdbtFormula + `'
          and j.value::jsonb->'version'='1'::jsonb
          and j.value::jsonb->'generation'=to_jsonb(s.source_generation)
          and not exists(select from hdbt_backfill)
          and not exists(select from hdbt_dirty)
          and not exists(
              select from unnest(array['settings','daily_scores','hdbt_daily_metrics',
                   'hdbt_state','hdbt_backfill','hdbt_dirty']) as r(name)
              where to_regclass(r.name) is distinct from to_regclass(format('%I.%I',current_schema(),r.name)))
    ))`

// readDailyCatalogRows executes a caller-owned SELECT over the shared daily
// relation. Its readiness check and values use one statement snapshot. A
// missing relation, invalid journal or missing grant restores the old reader.
func readDailyCatalogRows[T any](ctx context.Context, s *DB, query string, scan pgx.RowToFunc[T], args ...any) ([]T, error) {
	type querier interface {
		Query(context.Context, string, ...any) (pgx.Rows, error)
	}
	read := func(db querier, source string) ([]T, error) {
		rows, err := db.Query(ctx, fmt.Sprintf(query, source), args...)
		if err != nil {
			return nil, err
		}
		return pgx.CollectRows(rows, scan)
	}
	if s.profileGraphSchema != "" {
		return read(s.pool, "daily_scores")
	}
	var result []T
	var err error
	if unit, insideTransaction := s.pool.(*cacheTransaction); insideTransaction {
		// Protect an existing transaction from an unavailable publication.
		err = pgx.BeginFunc(ctx, unit, func(tx pgx.Tx) error {
			var readErr error
			result, readErr = read(tx, dailyCatalogSource)
			return readErr
		})
	} else {
		result, err = read(s.pool, dailyCatalogSource)
	}
	if err != nil {
		return read(s.pool, "daily_scores")
	}
	return result, nil
}
