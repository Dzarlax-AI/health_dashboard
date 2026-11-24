package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// MetricQuantityPoint is a chart value without observation extrema.
type MetricQuantityPoint struct {
	Date string  `json:"date"`
	Qty  float64 `json:"qty"`
}

type DailyStepPoint = MetricQuantityPoint

// LegacyMetricQuantityAggregation reports the quantity produced by the v1
// combined-series reader; v1 compatibility metrics average their buckets.
func LegacyMetricQuantityAggregation(metric string) string {
	if metric != "step_count" && usesMetricCompatibility(metric) {
		return "AVG"
	}
	return aggFuncFor(metric)
}

// MetricQuantitiesForContract accepts inclusive calendar dates. An empty start
// includes the whole history. Daily totals use the same journal and publication
// checks as the compatibility chart, including observed zeros and corrections.
func (s *DB) MetricQuantitiesForContract(ctx context.Context, metric, from, to, bucket, agg string, legacy bool) ([]MetricQuantityPoint, error) {
	if metric == "step_count" && bucket == "day" && agg == "SUM" {
		return s.DailyStepSeries(ctx, from, to)
	}
	points, err := s.MetricDataForContractContext(ctx, metric, from, to+" 23:59:59", bucket, agg, legacy)
	if err != nil {
		return nil, err
	}
	values := make([]MetricQuantityPoint, len(points))
	for i, point := range points {
		qty := point.Qty
		switch agg {
		case "MIN":
			qty = point.Min
		case "MAX":
			qty = point.Max
		}
		values[i] = MetricQuantityPoint{Date: point.Date, Qty: qty}
	}
	return values, nil
}

const publishedStepSeries = `select date, steps::float8 as qty from daily_scores
	where date between $1 and $2 and steps is not null`

// DailyStepSeries uses a single database snapshot so a concurrent source edit
// cannot land between checking its dirty generation and reading the rollup.
func (s *DB) DailyStepSeries(ctx context.Context, from, to string) ([]DailyStepPoint, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var points []DailyStepPoint
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		points, err = s.dailyStepSeriesTx(ctx, tx, from, to)
		return err
	})
	return points, err
}

func (s *DB) dailyStepSeriesTx(ctx context.Context, tx pgx.Tx, from, to string) ([]DailyStepPoint, error) {
	if s.profileGraphSchema != "" {
		unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}, profileGraphSchema: s.profileGraphSchema}
		return unit.catalogStepHistory(ctx, from, to)
	}
	var state CacheMaintenanceState
	var raw string
	err := tx.QueryRow(ctx, "select value from settings where key=$1", cacheMaintenanceSettingKey).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	// Missing or unrecognizable journal metadata cannot certify the rollup.
	var generation *uint64
	if err == nil {
		var decodeErr error
		state, decodeErr = decodeCacheMaintenanceState(raw)
		if decodeErr == nil {
			generation = &state.Generation
		}
	}
	series, enabled, err := readHDBTSteps(ctx, tx, from, to, generation)
	var dirty []string
	var unverifiedHistory bool
	if errors.Is(err, errHDBTStepEvidenceUnavailable) {
		// Go may have acknowledged a date that dbt still needs to correct.
		// Do not retain its daily-only value without the independent dbt fence.
		lookupErr := pgx.BeginFunc(ctx, tx, func(unit pgx.Tx) error {
			var err error
			dirty, err = hdbtDirtyStepDates(ctx, unit, from, to)
			return err
		})
		unverifiedHistory = lookupErr != nil
	} else if err != nil {
		return nil, err
	}
	if enabled {
		return series, nil
	}
	args := []any{from, to}
	query := publishedStepSeries
	if unverifiedHistory || state.TargetIdentity != "" || state.CompletedIdentity != CacheMaintenanceIdentity() {
		query = "select day as date, source_total::real::float8 as qty from (" +
			physicalStepTotalsSQL("substring(p.date,1,10) between $1 and $2", "hourly_metrics", "substring(hour,1,10) between $1 and $2") + ") recomputed"
	} else {
		for day := range state.Dirty {
			if from <= day && day <= to {
				dirty = append(dirty, day)
			}
		}
		if len(dirty) > 0 {
			args = append(args, dirty)
			query += " and date <> all($3::text[]) union all select day as date, source_total::real::float8 as qty from (" +
				physicalStepTotalsSQL("substring(p.date,1,10)=any($3::text[])", "hourly_metrics", "substring(hour,1,10)=any($3::text[])") + ") recomputed"
		}
	}
	rows, err := tx.Query(ctx, query+" order by date", args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[DailyStepPoint])
}

// Retain the observation calendar and genuine extrema while sharing certified
// daily quantities. Both reads see the same source, generation and publication.
func (s *DB) stepObservationsWithDailyQuantities(ctx context.Context, from, endDate, endTimestamp string) ([]DataPoint, error) {
	var points []DataPoint
	err := pgx.BeginTxFunc(ctx, s.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}, profileGraphSchema: s.profileGraphSchema}
		var err error
		points, err = unit.stepChart(from, endTimestamp, "day")
		if err != nil || len(points) == 0 {
			return err
		}
		daily, err := unit.dailyStepSeriesTx(ctx, tx, from, endDate)
		if err != nil {
			return err
		}
		// Both queries order their rows chronologically.
		i := 0
		for k, point := range points {
			for i < len(daily) && daily[i].Date < point.Date {
				i++
			}
			if i < len(daily) && daily[i].Date == point.Date {
				points[k].Qty = daily[i].Qty
			}
		}
		return nil
	})
	return points, err
}

// catalogStepHistory exercises the same typed read surface as other catalog
// consumers. DailyStepSeries pins one snapshot across the entire chart.
func (s *DB) catalogStepHistory(ctx context.Context, from, to string) ([]DailyStepPoint, error) {
	req := CatalogMetricRequest{MetricID: "daily.steps", Source: "daily_metrics", FromDate: from, ToDate: to, Limit: 256}
	var points []DailyStepPoint
	for {
		page, err := s.ReadCatalogMetricPage(ctx, req)
		if err != nil {
			return nil, err
		}
		for _, sample := range page.Samples {
			if sample.Status == "missing" {
				continue
			}
			if sample.Status != "available" || sample.Number == nil {
				return nil, fmt.Errorf("unexpected steps sample status %q", sample.Status)
			}
			var key struct {
				Date string `json:"date"`
			}
			if err = json.Unmarshal(sample.SampleKey, &key); err != nil {
				return nil, err
			}
			if _, err = time.Parse(time.DateOnly, key.Date); err != nil {
				return nil, err
			}
			qty, err := sample.Number.Float64()
			if err != nil {
				return nil, err
			}
			points = append(points, DailyStepPoint{Date: key.Date, Qty: float64(float32(qty))})
		}
		if page.Next == nil {
			return points, nil
		}
		req.After = page.Next
	}
}
