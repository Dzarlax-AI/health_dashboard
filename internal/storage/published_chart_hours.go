package storage

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const SettingHourlyChartReads = "dbt_hourly_chart_reads_v1"

// Only chartReadPool instances bound to a certified publication can select
// this relation. All query builders use one of these two fixed identifiers.
func (s *DB) chartHoursRelation() string {
	if p, ok := s.pool.(chartReadPool); ok && p.publishedHours {
		return "hdbt_hourly_metrics"
	}
	return "hourly_metrics"
}

func readChartHours[T any](ctx context.Context, s *DB, metric, from, to, bucket string, read func(*DB) (T, error)) (T, error) {
	fallback := func() (T, error) { return read(s.chartReader(ctx)) }
	// The wall-hour table cannot recover individual samples, aliases, physical
	// step hours, or the division between live and archived hourly totals.
	if (bucket != "hour" && bucket != "day") || metric == "step_count" || usesMetricCompatibility(metric) || usesCurrentHourlyMetric(metric) || s.profileGraphSchema != "" {
		return fallback()
	}
	end, wholeDay := strings.CutSuffix(to, " 23:59:59")
	if _, err := time.Parse(time.DateOnly, from); err != nil {
		return fallback()
	}
	if _, err := time.Parse(time.DateOnly, end); !wholeDay || err != nil {
		return fallback()
	}
	var result T
	var enabled bool
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `select exists(select from hdbt_state s
    join settings f on f.key=$1 and f.value='true'
    join settings j on j.key=$2 where s.singleton and s.shadow_enabled and s.initialized and s.ready and s.formula_id=$3
     and j.value::jsonb->'version'='1'::jsonb and j.value::jsonb->'generation'=to_jsonb(s.source_generation)
     and not exists(select from hdbt_backfill) and not exists(select from hdbt_dirty)
     and not exists(select from unnest(array['settings','hdbt_state','hdbt_hourly_metrics','hdbt_dirty','hdbt_backfill']) r(name)
       where to_regclass(r.name) is distinct from to_regclass(format('%I.%I',current_schema(),r.name)) or to_regclass(r.name) is null))`, SettingHourlyChartReads, cacheMaintenanceSettingKey, hdbtFormula).Scan(&enabled); err != nil || !enabled {
			return err
		}
		var err error
		result, err = read(&DB{pool: chartReadPool{storagePool: &cacheTransaction{Tx: tx, ctx: ctx}, ctx: ctx, publishedHours: true}})
		return err
	})
	if err == nil && enabled {
		return result, nil
	}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	return fallback()
}
