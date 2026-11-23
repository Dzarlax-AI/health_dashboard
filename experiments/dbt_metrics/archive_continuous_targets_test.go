package dbtmetrics_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
	"health-receiver/internal/storage"
)

// Keep the original writer's date-keyed Go operands independent of the dbt target model.
func TestPrepareArchiveContinuousTargets(t *testing.T) {
	ctx := context.Background()
	pool := markedArchivePool(t)
	var first, last string
	if err := pool.QueryRow(ctx, "select min(date),max(date) from daily_scores").Scan(&first, &last); err != nil {
		t.Fatal(err)
	}
	rows, err := storage.NewFromPool(pool).LoadSleepRows(first, last)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "create table replay.sleep_operands(day text primary key,eligible bool,reason text,value float8)"); err != nil {
		t.Fatal(err)
	}
	var data [][]any
	for _, row := range rows {
		decision := health.ComputeSleepEfficiency(row)
		data = append(data, []any{row.Date, decision.Eligible, decision.EligibilityReason, decision.Efficiency})
	}
	if _, err = pool.CopyFrom(ctx, pgx.Identifier{"replay", "sleep_operands"}, []string{"day", "eligible", "reason", "value"}, pgx.CopyFromRows(data)); err != nil {
		t.Fatal(err)
	}
}
