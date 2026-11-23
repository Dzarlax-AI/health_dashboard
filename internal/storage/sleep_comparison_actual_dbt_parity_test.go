package storage

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// This test only connects to the marked disposable database created by its driver.
func TestSleepComparisonActualDBTParity(t *testing.T) {
	port, token := os.Getenv("SLEEP_REFERENCE_PILOT_PORT"), os.Getenv("SLEEP_REFERENCE_PILOT_MARKER")
	if port == "" || token == "" {
		t.Skip("run experiments/dbt_metrics/scripts/run_sleep_reference.sh")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(fmt.Sprintf("postgres://synthetic_admin@127.0.0.1:%s/sleep_reference_lab?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = "source_data,public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	var actualToken string
	if err = pool.QueryRow(ctx, "select token from public.synthetic_sleep_reference").Scan(&actualToken); err != nil || actualToken != token {
		t.Fatal("synthetic marker mismatch", err)
	}
	// Runtime reads its own installed view, built from the same embedded macro.
	db := &DB{pool: pool}
	if err = db.ensureSleepDurationReference(ctx); err != nil {
		t.Fatal(err)
	}
	date, _ := time.Parse(time.DateOnly, "2026-10-26")
	insert := func(day string, hours float64) {
		t.Helper()
		_, err := pool.Exec(ctx, `insert into source_data.completed_night_sleep(wake_date,duration_hours,source,source_epoch,algorithm_version,input_hash,capture_completeness,duration_assessment,finalization_state,claim_eligibility) values($1,$2,'Watch','epoch','v1',$1,'complete','plausible','final','eligible')`, day, hours)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert(date.Format(time.DateOnly), 7.5)
	for i := 1; i <= 6; i++ {
		insert(date.AddDate(0, 0, -i).Format(time.DateOnly), 7.5)
	}
	type comparison struct {
		Status                     string
		Count                      int
		Duration, Reference, Delta *int
	}
	check := func(status string, count int, duration, reference, delta *int) string {
		t.Helper()
		want := comparison{status, count, duration, reference, delta}
		f := db.GetSleepDurationComparison(ctx, date.Format(time.DateOnly))
		if f == nil || f.EligibleNights == nil {
			t.Fatalf("runtime missing result: %+v", f)
		}
		runtime := comparison{f.Status, *f.EligibleNights, f.DurationMinutes, f.ReferenceMinutes, f.DeltaMinutes}
		var model comparison
		err := pool.QueryRow(ctx, `select status,eligible_nights,duration_minutes,reference_minutes,delta_minutes from dbt_output.semantic_sleep_duration_comparison_v1 where wake_date=$1`, date.Format(time.DateOnly)).Scan(&model.Status, &model.Count, &model.Duration, &model.Reference, &model.Delta)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(runtime, want) || !reflect.DeepEqual(model, want) {
			t.Fatalf("expected=%+v runtime=%+v dbt=%+v", want, runtime, model)
		}
		return f.FactVersion
	}
	ptr := func(n int) *int { return &n }
	check("insufficient_history", 6, ptr(450), nil, nil)
	// Seventh night arrives late; half-minute values exercise independent rounding and signed deltas.
	insert(date.AddDate(0, 0, -7).Format(time.DateOnly), 7.5)
	check("available", 7, ptr(450), ptr(450), ptr(0))
	// Rounding each input gives +1/-1, whereas rounding their raw difference gives 0.
	setDurations := func(current, history float64) {
		t.Helper()
		_, err := pool.Exec(ctx, `update source_data.completed_night_sleep set duration_hours=case when wake_date=$1 then $2::double precision else $3::double precision end`, date.Format(time.DateOnly), current/60, history/60)
		if err != nil {
			t.Fatal(err)
		}
	}
	setDurations(450.6, 450.4)
	check("available", 7, ptr(451), ptr(450), ptr(1))
	setDurations(450.4, 450.6)
	check("available", 7, ptr(450), ptr(451), ptr(-1))
	setDurations(449.5, 450)
	before := check("available", 7, ptr(450), ptr(450), ptr(0))
	_, err = pool.Exec(ctx, `update source_data.completed_night_sleep set duration_hours=8.5 where wake_date=$1`, date.AddDate(0, 0, -7).Format(time.DateOnly))
	if err != nil {
		t.Fatal(err)
	}
	after := check("available", 7, ptr(450), ptr(459), ptr(-9))
	if before == after {
		t.Fatal("late correction did not change fact version")
	}
}
