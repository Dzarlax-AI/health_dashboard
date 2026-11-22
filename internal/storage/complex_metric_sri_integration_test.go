package storage

import (
	"context"
	"math"
	"os"
	"testing"
)

func TestSleepRegularityShadow(t *testing.T) {
	if os.Getenv("PREFERRED_SLEEP_FIXTURE_TOKEN") == "" {
		t.Skip("synthetic dbt capture required")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	id := insertTestRawRecord(t, db, "sri-synthetic")
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into metric_points(health_record_id,metric_name,units,date,qty,source,quality)
        select $1, metric,'hr',(current_date-n)::text || wall_time, qty, source,'ok'
        from generate_series(2,13) n cross join (values
          ('sleep_total',' 23:30:00 +0200',2.0,'Apple Watch'),
          ('sleep_total',' 21:10:00 +0200',4.0,'Ring'),
          ('sleep_total',' 00:00:00 +0200',7.0,'Summary'),
          ('sleep_deep',' 03:10:00 +0200',0.5,'Apple Watch')
        ) s(metric,wall_time,qty,source) where n<>6`, id)
	compare := func(capture string) float64 {
		t.Helper()
		exec(`insert into source_data.sri_captures values('synthetic',$1,current_date,14,$2)`, capture, db.GetSetting(PreferredSleepSourceSetting, ""))
		exec(`insert into source_data.sri_segments select 'synthetic',$1,left(date,19)::timestamp,source,metric_name,qty,quality from metric_points where health_record_id=$2`, capture, id)
		want, n, ok := db.ComputeSRI(14)
		var got *float64
		var nights *int
		var status string
		if err := db.pool.QueryRow(ctx, `select sri,nights,status from dbt_output.sleep_regularity_index_v1 where profile='synthetic' and capture=$1`, capture).Scan(&got, &nights, &status); err != nil {
			t.Fatal(err)
		}
		if nights == nil || n != *nights || ok != (got != nil) || ok && (status != "available" || math.Abs(*got-want) > 1e-9) || !ok && status != "insufficient_days" {
			t.Fatalf("capture %s: Go (%v,%d,%t), SQL (%v,%v,%s)", capture, want, n, ok, got, nights, status)
		}
		return want
	}
	compare("mixed_sources_and_gap")
	exec(`update metric_points set qty=3.7 where source='Apple Watch' and metric_name='sleep_total' and left(date,10)=(current_date-4)::text`)
	winnerScore := compare("late_correction")
	exec(`insert into metric_points(health_record_id,metric_name,units,date,qty,source,quality)
        select health_record_id,metric_name,units,replace(date,'23:30:00','13:30:00'),qty,'Ä Ultra',quality
        from metric_points where source='Apple Watch' and metric_name='sleep_total'`)
	// With the rival present the score must match the Apple Watch alone.
	if got := compare("equal_rank"); got != winnerScore {
		t.Fatalf("tie selected a different source: got %v, want %v", got, winnerScore)
	}
	if err := db.SaveSettings(map[string]string{PreferredSleepSourceSetting: "Ä Ultra"}); err != nil {
		t.Fatal(err)
	}
	preferredScore := compare("preferred_rival")
	if err := db.SaveSettings(map[string]string{PreferredSleepSourceSetting: "unobserved"}); err != nil {
		t.Fatal(err)
	}
	if got := compare("preferred_missing"); got != winnerScore {
		t.Fatalf("missing source changed SRI: %v vs %v", got, winnerScore)
	}
	exec(`delete from metric_points where source='Apple Watch'`)
	if rival := compare("other_source_only"); rival == winnerScore || rival != preferredScore {
		t.Fatal("fixture should distinguish the two source-specific sleep masks")
	}
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "alter table settings rename to settings_unavailable"); err != nil {
		t.Fatal(err)
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	if _, _, ok := unit.ComputeSRI(14); ok {
		t.Fatal("SRI should be unavailable if the source preference cannot be read")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	exec(`delete from metric_points where left(date,10)<(current_date-3)::text`)
	compare("withdrawal")
}
