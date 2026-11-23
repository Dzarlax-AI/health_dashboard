package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	dbtmetrics "health-receiver/experiments/dbt_metrics"
)

const storedStepsSQL = "('step_count', '" + archiveSamplePrefix + "step_count')"

// Only a live v2 total can represent an observed zero.
func usableStepPointSQL(alias string) string {
	return alias + ".metric_name IN " + storedStepsSQL + " AND " + alias + ".quality='ok' AND (" +
		alias + ".qty>0 OR (" + alias + ".qty=0 AND " + alias + ".origin='live' AND EXISTS (" +
		"SELECT FROM health_records hr WHERE hr.id=" + alias + ".health_record_id AND hr.content_type='application/json; metrics-contract=2')))"
}

// Only hours that can reproduce the existing wall-hour cache may be rebuilt
// from an incremental mutation. A cache row may outlive its raw inputs.
func stepHourProofCTEs() string {
	return `hours AS (SELECT DISTINCT substring(date,1,13)||':00' AS hour FROM keys),
		wall_cache AS (
			SELECT c.* FROM hours h JOIN hourly_metrics c USING(hour) WHERE c.metric_name='step_count'
		), step_input AS (` + dbtmetrics.StepPointInputsSQL("metric_points", "health_records",
		"EXISTS (SELECT FROM wall_cache c WHERE substring(p.date,1,13)=substring(c.hour,1,13) AND substring(p.date,1,10)=substring(c.hour,1,10))") + `),
		incomplete AS (` + dbtmetrics.IncompleteStepHoursSQL("step_input", "wall_cache") + `)`
}

func previousStepPoints(ctx context.Context, tx pgx.Tx, points []MetricPoint) (json.RawMessage, error) {
	var keys [][2]string
	for _, p := range points {
		if p.MetricName == "step_count" {
			keys = append(keys, [2]string{p.Date, p.Source})
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	payload, err := json.Marshal(keys)
	if err != nil {
		return nil, err
	}
	var previous json.RawMessage
	err = tx.QueryRow(ctx, `WITH keys AS (
		SELECT DISTINCT k->>0 AS date,k->>1 AS source FROM jsonb_array_elements($1::jsonb) k
	), `+stepHourProofCTEs()+`
	SELECT jsonb_agg(jsonb_build_array(k.date,k.source,p.qty,p.health_record_id,p.origin,p.units))
	FROM keys k LEFT JOIN metric_points p ON p.metric_name='step_count' AND p.date=k.date AND p.source=k.source
	WHERE NOT EXISTS (SELECT FROM incomplete i WHERE i.hour=substring(k.date,1,13)||':00')
	  AND (p.id IS NOT NULL OR EXISTS (SELECT FROM wall_cache c WHERE c.hour=substring(k.date,1,13)||':00'))`, json.RawMessage(payload)).Scan(&previous)
	return previous, err
}

func repairLiveStepReplacements(ctx context.Context, tx pgx.Tx, recordID int64, previous json.RawMessage) error {
	if len(previous) == 0 {
		return nil
	}
	var cells json.RawMessage
	err := tx.QueryRow(ctx, `SELECT jsonb_agg(DISTINCT jsonb_build_array(substring(p.date,1,16),coalesce(p.source,'')))
		FROM jsonb_array_elements($1::jsonb) k JOIN metric_points p ON p.metric_name='step_count' AND p.date=k->>0 AND p.source=k->>1
		LEFT JOIN health_records old ON old.id=(k->>3)::bigint
		JOIN health_records current ON current.id=p.health_record_id
		WHERE p.health_record_id=$2 AND (p.qty IS DISTINCT FROM (k->>2)::real OR
			p.origin IS DISTINCT FROM (k->>4) OR p.units IS DISTINCT FROM (k->>5) OR
			(old.content_type='application/json; metrics-contract=2') IS DISTINCT FROM
			(current.content_type='application/json; metrics-contract=2'))`, previous, recordID).Scan(&cells)
	if err != nil {
		return err
	}
	return repairWithdrawnSteps(ctx, tx, cells)
}

// Source mutations return only distinct (minute, source) cells that lost a
// usable step point. Rebuild their hours after ALL upserts in the transaction.
// Missing input outside these cells can be a transient sync gap.
func repairWithdrawnSteps(ctx context.Context, tx pgx.Tx, groups ...json.RawMessage) error {
	var nonempty []json.RawMessage
	for _, group := range groups {
		if len(group) > 0 {
			nonempty = append(nonempty, group)
		}
	}
	if len(nonempty) == 0 {
		return nil
	}
	payload, err := json.Marshal(nonempty)
	if err != nil {
		return err
	}
	const cells = `with cells as (
		select distinct entry->>0 as minute, entry->>1 as source
		from jsonb_array_elements($1::jsonb) g, jsonb_array_elements(g) entry
	), hours as (
		select distinct substring(minute,1,13) || ':00' as hour, source from cells
	)`
	for _, query := range []string{
		cells + `delete from minute_metrics m using cells c where m.metric_name='step_count'
			and m.minute=c.minute and m.source=c.source`,
		cells + `delete from hourly_metrics h using hours a where h.metric_name='step_count'
			and h.hour=a.hour and h.source=a.source`,
		cells + `, step_points AS (
			SELECT 'step_count'::text AS metric_name, i.* FROM (` +
			dbtmetrics.StepPointInputsSQL("metric_points", "health_records",
				"EXISTS (SELECT FROM hours h WHERE h.source=coalesce(p.source,'') AND substring(p.date,1,10)=substring(h.hour,1,10) AND substring(p.date,1,13)=substring(h.hour,1,13))") + `) i
		) insert into hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count) ` + dbtmetrics.StepWallRowsSQL("step_points"),
		cells + `, days as (select distinct substring(hour,1,10) as day from hours)
			update daily_scores d set computed_at=now()::text, steps=(
				select source_total from (` + physicalStepTotalsSQL("substring(p.date,1,10)=d.date", "hourly_metrics", "substring(hour,1,10)=d.date") + `) totals) from days where d.date=days.day`,
	} {
		if _, err := tx.Exec(ctx, query, json.RawMessage(payload)); err != nil {
			return fmt.Errorf("repair withdrawn step cells: %w", err)
		}
	}
	if err := MarkMetricCatalogHoursChangedTx(ctx, tx); err != nil {
		return err
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	return unit.writeDashboardSnapshot(ctx)
}
