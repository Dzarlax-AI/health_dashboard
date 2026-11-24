package storage

import (
	"context"
	"testing"
)

func TestPublishedChartHoursFreshnessAndSources(t *testing.T) {
	db, done := testDB(t)
	defer done()
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	const metric = "test_pulse"
	const day = "2026-07-01"
	const end = day + " 23:59:59"
	exec(`insert into hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count) values($1,$2,'watch|device',10,8,12,1)`, metric, day+" 01:00")
	exec(`insert into hdbt_hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count)
 values($1,$2,'watch|device',20,18,22,1),($1,$2,'ring',40,38,42,1)`, metric, day+" 01:00")
	state := emptyCacheMaintenanceState()
	state.Generation = 42
	setDailyStepSeriesJournal(t, db, state)
	exec(`update hdbt_state set shadow_enabled=true`)
	exec(`update hdbt_state set initialized=true,ready=true,formula_id=$1,source_generation=42`, hdbtFormula)
	if err := db.SaveSettings(map[string]string{SettingHourlyChartReads: "true"}); err != nil {
		t.Fatal(err)
	}
	check := func(from, to string, want float64) {
		t.Helper()
		for _, bucket := range []string{"hour", "day"} {
			pts, err := db.MetricDataForContractContext(ctx, metric, from, to, bucket, "AVG", false)
			if err != nil || len(pts) != 1 || pts[0].Qty != want {
				t.Fatalf("%s got %+v %v, want %g", bucket, pts, err, want)
			}
			src, err := db.MetricDataBySourceForContractContext(ctx, metric, from, to, bucket, "AVG", false)
			if err != nil {
				t.Fatal(err)
			}
			if want == 30 {
				if pts[0].Min != 18 || pts[0].Max != 42 || len(src) != 2 || src[0].Source != "ring" || src[0].Points[0].Qty != 40 || src[1].Source != "watch" || src[1].Points[0].Qty != 20 {
					t.Fatalf("published bounds or sources lost: %+v %+v", pts, src)
				}
			} else if len(src) != 1 || src[0].Source != "watch" || src[0].Points[0].Qty != 10 {
				t.Fatalf("wrong fallback sources: %+v", src)
			}
		}
	}
	check(day, end, 30)
	check(day, day+" 01:30:00", 10)
	exec(`insert into hdbt_dirty(date,generation) values($1,43)`, day)
	check(day, end, 10)
	exec(`delete from hdbt_dirty where date=$1`, day)
	exec(`update hdbt_state set source_generation=41`)
	check(day, end, 10)
	exec(`update hdbt_state set source_generation=42`)
	if err := db.SaveSettings(map[string]string{SettingHourlyChartReads: "false"}); err != nil {
		t.Fatal(err)
	}
	check(day, end, 10)
}
