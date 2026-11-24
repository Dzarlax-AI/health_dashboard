package storage

import "testing"

func checkNativeHRV(t *testing.T, db *DB) {
	t.Helper()
	ctx := t.Context()
	exec := func(q string) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into workouts(external_id,name,start_time,end_time,duration_sec)
 values('typed-hrv','run','2026-01-04 08:00Z','2026-01-04 08:30Z',1800);
 insert into daily_scores(date,hrv_avg) values('2026-01-05',42),('2026-01-06',42);
 insert into settings(key,value) values('cache_maintenance_hrv_repair_v1:2026-01-06','1');
 with receipt as (
 insert into health_records(automation_name,content_type,payload)
 values('synthetic','application/json','{"data":{"metrics":[{"name":"heart_rate_variability","units":"ms","data":[
 {"date":"2026-01-04 08:05:00 +0000","source":"clock","qty":10,"method":"sdnn","end_at":"2026-01-04T08:06:00Z"},
 {"date":"2026-01-04 08:40:00 +0000","source":"clock","qty":20,"method":"sdnn"},
 {"date":"2026-01-04 08:50:00 +0000","source":"ring","qty":60}]}]}}') returning id)
 insert into metric_points(metric_name,date,source,units,qty,health_record_id,origin,quality)
 select 'heart_rate_variability',p.date,p.source,'ms',p.qty,r.id,'live','ok'
 from receipt r cross join(values('2026-01-04 08:05:00 +0000','clock',10),
 ('2026-01-04 08:40:00 +0000','clock',20),('2026-01-04 08:50:00 +0000','ring',60)) p(date,source,qty)`)
	checks := []struct{ id, key, value string }{
		{"hrv_daily_live_compat_v1", `{"date":"2026-01-04"}`, "37.5"},
		{"hrv_morning_compat_v1", `{"date":"2026-01-04"}`, "30"},
		{"hrv_morning_compat_v1", `{"date":"2026-01-05"}`, "42"},
		{"hrv_morning_compat_v1", `{"date":"2026-01-06"}`, "missing"},
		{"hrv_fixed_clock_day_median_v1", `{"date":"2026-01-04"}`, "20"},
		{"hrv_observation_unknown_v1", `{"source":"ring"}`, "60"},
		{"hrv_workout_sdnn_v1", `{"source":"clock","component":"interval_mean_ms"}`, "10"},
		{"hrv_after_workout_sdnn_v1", `{"source":"clock","component":"timestamp_mean_ms"}`, "20"},
	}
	for _, c := range checks {
		var ok bool
		err := db.pool.QueryRow(ctx, `select count(*)=1 and coalesce(bool_and(
   case when $3::text='missing' then value_status='missing' else value_status='available' and numeric_value=($3::text)::float8 end),false)
   from tenant_native_metric_read_v1 where metric_id=$1 and sample_key @> $2::jsonb`, c.id, c.key, c.value).Scan(&ok)
		if err != nil || !ok {
			t.Fatalf("%s %s wanted %s: match=%v err=%v", c.id, c.key, c.value, ok, err)
		}
	}
}
