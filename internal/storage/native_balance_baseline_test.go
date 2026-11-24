package storage

import (
	"testing"
	"time"
)

func checkNativeBalanceAndBaseline(t *testing.T, db *DB) {
	t.Helper()
	ctx := t.Context()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("insert into settings(key,value) values($1,'true') on conflict(key) do update set value='true'", SettingDecisionCapture)
	exec("delete from settings where key='timezone'")
	t.Setenv("REPORT_TZ", "Europe/Belgrade")
	exec(`insert into sleep_goal values('2028-01-01',8,'test',now(),now());
        insert into sleep_period_coverage(wake_date,source_epoch,capture_completeness,coverage_generation,
          covered_interval_start,covered_interval_end,observed_at,input_hash)
        select (date '2028-10-30'-n)::text,'test','complete','test',now()-interval '24 hours',now(),now(),'test'
        from generate_series(0,13)n`)
	date := time.Date(2028, 10, 30, 15, 0, 0, 0, time.UTC)
	result, err := db.ReconcileSleepDurationBalance(ctx, "2028-10-30", date)
	if err != nil || result.BalanceHours == nil || *result.BalanceHours != -112 {
		t.Fatalf("sleep balance: %+v %v", result, err)
	}
	exec(`update sleep_goal set goal_hours=7 where effective_date='2028-01-01';
        with r as(insert into health_records(automation_name,content_type,payload) values('test','application/json','{}') returning id)
        insert into metric_points(metric_name,date,source,units,qty,quality,health_record_id)
        select 'heart_rate_variability',(date '2028-10-30'-n)::text||' 08:00:00 +0200','test','ms',30+n,'ok',id from r cross join generate_series(1,7)n`)
	b, ok, err := db.PersonalBaselineContext(ctx, "2028-10-30", ChannelHRV, 30, db.reportTZLocation())
	if err != nil || !ok || b.Median != 34 {
		t.Fatalf("HRV baseline: %+v %v %v", b, ok, err)
	}
	exec("update metric_points set qty=120 where metric_name='heart_rate_variability' and date like '2028-%'")
	for _, c := range []struct {
		id   string
		want float64
	}{
		{"sleep_balance.balance_hours", -112}, {"sleep_balance_day.goal_hours", 8}, {"sleep_balance_day.delta_hours", -8},
		{"personal_baseline.median", 34}, {"hrv_fixed_window_baseline_v1", 34}, {"personal_baseline.sample_count", 7},
	} {
		var matched bool
		if err := db.pool.QueryRow(ctx, `select exists(select from tenant_native_metric_read_v1 where metric_id=$1
           and numeric_value=$2 and sample_key->>'capture' like '2028-%')`, c.id, c.want).Scan(&matched); err != nil || !matched {
			t.Fatalf("%s: %v %v", c.id, matched, err)
		}
	}
	var zone string
	if err := db.pool.QueryRow(ctx, "select inputs->>'zone' from hdbt_decision_inputs where metric='sleep_balance' and decision_key='2028-10-30'").Scan(&zone); err != nil || zone != "Europe/Belgrade" {
		t.Fatalf("balance timezone=%s: %v", zone, err)
	}
}
