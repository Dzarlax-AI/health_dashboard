package storage

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	dbtmetrics "health-receiver/experiments/dbt_metrics"
)

type stepDateQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func stepMutationDates(points []MetricPoint) []string {
	var dates []string
	for _, p := range points {
		if (p.MetricName == "step_count" || p.MetricName == archiveSamplePrefix+"step_count") && len(p.Date) >= 10 {
			dates = append(dates, p.Date[:10])
		}
	}
	return uniqueStepDates(dates)
}

func uniqueStepDates(dates []string) []string {
	slices.Sort(dates)
	return slices.Compact(dates)
}

// Only the boundary of a capture can transfer a measurement to a date outside
// it. Two legal offsets differ by at most 47:58. Even a multi-year continuous
// import checks at most four boundary dates plus their immediate neighbors.
func stepAliasWindow(dates []string) (boundary, nearby []string) {
	covered := make(map[string]bool, len(dates))
	for _, day := range dates {
		covered[day] = true
	}
	for day := range covered {
		d, err := time.Parse(time.DateOnly, day)
		if err != nil {
			continue
		}
		for _, n := range []int{-2, -1, 1, 2} {
			if !covered[d.AddDate(0, 0, n).Format(time.DateOnly)] {
				boundary = append(boundary, day)
				for k := -2; k <= 2; k++ {
					nearby = append(nearby, d.AddDate(0, 0, k).Format(time.DateOnly))
				}
				break
			}
		}
	}
	return uniqueStepDates(boundary), uniqueStepDates(nearby)
}

// Read the canonical projection on both sides of the boundary. Returning only
// outside dates keeps the existing journal and writer responsible for the capture.
func stepAliasDates(ctx context.Context, q stepDateQuerier, dates []string) ([]string, error) {
	boundary, nearby := stepAliasWindow(dates)
	if len(boundary) == 0 {
		return nil, nil
	}
	input := dbtmetrics.StepPointInputsSQL("metric_points", "health_records", "substring(p.date,1,10)=any($2::text[])")
	var overlap []string
	err := q.QueryRow(ctx, `with points as (`+input+`), pairs as (
		select substring(date,1,10) as original,substring(canonical_date,1,10) as winner
		from points where not counted and substring(date,1,10)<>substring(canonical_date,1,10)
	)
	select array(select distinct d.day from pairs
	cross join lateral (values(original),(winner)) d(day)
	where (original=any($1::text[]) or winner=any($1::text[]))
	and not d.day=any($3::text[]) order by d.day)`, boundary, nearby, dates).Scan(&overlap)
	return overlap, err
}

// The previous projection can release a date when a correction changes the
// quantity or grain. The new projection can acquire a previously unseen peer.
func insertPointsAndAliasDatesTx(ctx context.Context, tx pgx.Tx, recordID int64, points []MetricPoint, ordering ingestPointOrdering) ([]string, error) {
	dates := stepMutationDates(points)
	before, err := stepAliasDates(ctx, tx, dates)
	if err != nil {
		return nil, err
	}
	if err = insertPointsTx(ctx, tx, recordID, points, ordering); err != nil {
		return nil, err
	}
	after, err := stepAliasDates(ctx, tx, dates)
	return uniqueStepDates(append(before, after...)), err
}

func refreshStepAliasDates(ctx context.Context, tx pgx.Tx, dates []string) error {
	if len(dates) == 0 {
		return nil
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	for _, day := range uniqueStepDates(dates) {
		if err := unit.clearWithdrawnStepHours(ctx, "AND substring(date,1,10)=$1", day); err != nil {
			return err
		}
		if err := unit.rebuildHistoricalHourlyMetric(ctx, day, "step_count"); err != nil {
			return err
		}
		if err := unit.rebuildHistoricalDailyMetric(ctx, day, "steps", "step_count"); err != nil {
			return err
		}
	}
	if err := MarkMetricCatalogHoursChangedTx(ctx, tx); err != nil {
		return err
	}
	return unit.writeDashboardSnapshot(ctx)
}

// A bounded historical rebuild touches other dates only for the same step instant.
func (s *DB) refreshHistoricalAliasNeighbors(ctx context.Context, day string) error {
	aliases, err := stepAliasDates(ctx, s.pool, []string{day})
	if err != nil {
		return err
	}
	return refreshStepAliasDates(ctx, s.pool.(*cacheTransaction).Tx, aliases)
}
