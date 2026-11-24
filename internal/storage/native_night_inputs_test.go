package storage

import (
	"strconv"
	"testing"
)

func checkNativeNightInputs(t *testing.T, db *DB) {
	t.Helper()
	ctx := t.Context()
	exec := func(q string) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into settings(key,value) values('timezone','UTC') on conflict(key) do update set value='UTC';
 insert into completed_night_sleep(wake_date,duration_hours,source,source_epoch,input_hash,capture_completeness,
 duration_assessment,finalization_state,claim_eligibility,observed_at,algorithm_version)
 select day,2,'watch','epoch','h-'||day,'complete','plausible','final','eligible',now(),'v1'
 from unnest(array['2026-04-01','2026-04-02','2026-04-03']) day;
 with receipt as (insert into health_records(automation_name,content_type,payload) values('synthetic','application/json','{}') returning id)
 insert into metric_points(metric_name,date,source,units,qty,origin,quality,health_record_id)
 select p.*,r.id from receipt r cross join (values
 ('sleep_core','2026-04-01 02:00:00 +0000','watch','hr',1,'live','ok'),
 ('sleep_deep','2026-04-01 03:00:00 +0000','watch','hr',1,'live','ok'),
 ('sleep_core','2026-04-02 02:00:00 +0000','watch','hr',2,'live','ok'),
 ('sleep_core','2026-04-02 06:00:00 +0000','watch','hr',2,'live','ok'),
 ('sleep_core','2026-04-03 00:00:00 +0000','watch','hr',2,'live','ok')) p(metric_name,date,source,units,qty,origin,quality);
 with receipt as (insert into health_records(automation_name,content_type,payload) values
 ('synthetic','application/json','{"data":{"metrics":[{"name":"heart_rate_variability","units":"ms","data":[{"qty":33,"date":"2026-04-01 02:03:00 +0000","source":"watch","method":"sdnn"}]}]}}') returning id)
 insert into metric_points(metric_name,date,source,units,qty,origin,quality,health_record_id)
 select 'heart_rate_variability','2026-04-01 02:03:00 +0000','watch','ms',33,'live','ok',id from receipt;
 insert into sleep_rollout_candidates(profile,id,revision,wake_date,priority,snapshot)
 select tenant_id,'["native","verified"]',1,'2026-04-01',1,
 '{"stages":[{"kind":"core","start_at":"2026-04-01T02:00:00Z","end_at":"2026-04-01T03:00:00Z"}]}'::jsonb
 from __tenant_identity where singleton`)
	checks := []struct{ id, key, value string }{
		{"main_sleep_episode_stages_v1", `{"wake_date":"2026-04-01"}`, "recorded_episode_stages"},
		{"main_sleep_stage_minutes_v1", `{"wake_date":"2026-04-01","component":"deep_minutes"}`, "60"},
		{"main_sleep_episode_stages_v1", `{"wake_date":"2026-04-02"}`, "no_detailed_stages"},
		{"main_sleep_episode_stages_v1", `{"wake_date":"2026-04-03"}`, "day_summary_only"},
		{"main_sleep_stage_minutes_v1", `{"wake_date":"2026-04-03","component":"core_minutes"}`, "120"},
		{"hrv_main_sleep_sdnn_v1", `{"wake_date":"2026-04-01","source":"watch"}`, "33"},
		{"shadow_sleep_daily_duration_v1", `{"wake_date":"2026-04-01"}`, "3600"},
	}
	for _, c := range checks {
		var ok bool
		var numeric *float64
		if n, err := strconv.ParseFloat(c.value, 64); err == nil {
			numeric = &n
		}
		err := db.pool.QueryRow(ctx, `select count(*)=1 and coalesce(bool_and(value_status='available' and
    case when numeric_value is not null then numeric_value=$4::float8 else text_value=$3 end),false)
    from tenant_native_metric_read_v1 where metric_id=$1 and sample_key @> $2::jsonb`, c.id, c.key, c.value, numeric).Scan(&ok)
		if err != nil || !ok {
			t.Fatalf("%s %s expected %s: match=%v error=%v", c.id, c.key, c.value, ok, err)
		}
	}
}
