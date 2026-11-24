package storage

import (
	"encoding/json"
	"testing"
)

func checkNativeSleepArchitecture(t *testing.T, db *DB) {
	t.Helper()
	ctx := t.Context()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("insert into settings(key,value) values($1,'true') on conflict(key) do update set value='true'", SettingDecisionCapture)
	const day = "2026-10-25"
	exec(`with receipt as (insert into health_records(automation_name,content_type,payload)
		values('synthetic','application/json','{}') returning id)
		insert into metric_points(metric_name,date,source,qty,health_record_id)
		select metric,ts,'Watch',qty,id from receipt cross join (values
		 ('sleep_deep','2026-10-24 23:00:00.000000007 +0200',2.25),
		 ('sleep_rem','2026-10-25 02:30:00.000000009 +0200',1),
		 ('sleep_core','2026-10-25 02:30:00.000000011 +0100',1),
		 ('sleep_awake','2026-10-25 02:45:00.000000003 +0200',0.125)
		) s(metric,ts,qty)`)
	type comparison struct {
		day, window string
		daily       map[string]any
		features    SleepArchitectureFeatureFields
	}
	load := func() comparison {
		t.Helper()
		days, err := db.LoadSleepArchitectureDays(day, day)
		if err != nil {
			t.Fatal(err)
		}
		features, err := db.LoadSleepArchitectureFeatures(day, day)
		if err != nil {
			t.Fatal(err)
		}
		d := days[day]
		if !d.Eligible || d.AsleepHours < 4 || features[day].ArchitectureEligibleDays7d != 1 {
			t.Fatalf("synthetic source: %+v", d)
		}
		r := comparison{features: features[day], daily: map[string]any{
			"asleep_hours": d.AsleepHours, "waso_hours": d.WASOHours, "explicit_wake_bouts": d.ExplicitWakeBouts,
			"gap_inferred_wake_bouts": d.GapInferredWakeBouts, "longest_wake_bout_hours": d.LongestWakeBoutHours,
			"fragmentation_index": d.FragmentationIndex, "eligible": d.Eligible, "reason": d.Reason, "confidence": d.Confidence}}
		for metric, out := range map[string]*string{"sleep_architecture_day": &r.day, "sleep_architecture_window": &r.window} {
			if err := db.pool.QueryRow(ctx, "select decision_key||':'||input_hash from hdbt_decision_inputs where metric=$1 and decision_key=$2 order by captured_at desc limit 1", metric, day).Scan(out); err != nil {
				t.Fatal(err)
			}
		}
		return r
	}
	check := func(r comparison) {
		t.Helper()
		for _, v := range []struct {
			model, key string
			expected   any
		}{{"sleep_architecture_days_v1", r.day, r.daily}, {"sleep_architecture_windows_v1", r.window, r.features}} {
			b, err := json.Marshal(v.expected)
			if err != nil {
				t.Fatal(err)
			}
			var matches bool
			if err := db.pool.QueryRow(ctx, `select jsonb_object_agg(split_part(metric_id,'.',2),value)=$3::jsonb
				from tenant_native_metric_read_v1 where source=$1 and sample_key->>'capture'=$2`, v.model, v.key, json.RawMessage(b)).Scan(&matches); err != nil || !matches {
				t.Fatalf("%s exact replay: %v, match %v", v.model, err, matches)
			}
		}
	}
	first := load()
	check(first)
	exec("update metric_points set qty=0.25 where metric_name='sleep_awake' and date='2026-10-25 02:45:00.000000003 +0200'")
	next := load()
	if next.day == first.day || next.window == first.window || *next.features.WASOHours7d == *first.features.WASOHours7d {
		t.Fatal("late segment did not produce new day and window versions")
	}
	check(next)
	check(first)
	var versions, windows int
	if err := db.pool.QueryRow(ctx, `select count(*) filter(where metric='sleep_architecture_day'),count(*) filter(where metric='sleep_architecture_window')
		from hdbt_decision_inputs where metric in ('sleep_architecture_day','sleep_architecture_window')`).Scan(&versions, &windows); err != nil || versions != 15 || windows != 2 {
		t.Fatalf("stored %d daily revisions and %d manifests: %v", versions, windows, err)
	}
	checkNativeSRI(t, db)

}

func checkNativeSRI(t *testing.T, db *DB) {
	t.Helper()
	ctx := t.Context()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// All earlier fixture rows and the mutations below belong to the caller's
	// rollback-only catalog integration test transaction.
	exec("delete from metric_points where metric_name in ('sleep_total','sleep_deep','sleep_core','sleep_unspecified','sleep_rem')")
	add := func(first, last int) {
		t.Helper()
		exec(`with receipt as (insert into health_records(automation_name,content_type,payload) values('synthetic','application/json','{}') returning id)
			insert into metric_points(metric_name,date,source,qty,health_record_id)
			select 'sleep_total',(current_date-i)::text||' 02:15:59.999999999 +0200','Watch',2,id
			from receipt cross join generate_series($1::int,$2::int) i`, first, last)
	}
	check := func(wantN int, wantAvailable bool) string {
		t.Helper()
		sri, n, ok := db.ComputeSRI(7)
		if ok != wantAvailable || n != wantN {
			t.Fatalf("live SRI %v nights %d, available %v", sri, n, ok)
		}
		var match bool
		var key string
		if err := db.pool.QueryRow(ctx, `with j as (select decision_key||':'||input_hash as k,result from hdbt_decision_inputs
			where metric='sri_window' order by captured_at desc limit 1)
			select k, count(*)=2 and bool_and(case m.metric_id when 'readiness.sleep_regularity_nights' then m.numeric_value=$1::int
				else (m.numeric_value is not distinct from (j.result->>'sri')::numeric) and m.value_status=$2 end)
			from j join tenant_native_metric_read_v1 m on m.source='sleep_regularity_index_v1' and m.sample_key->>'capture'=k
			group by k`, n, map[bool]string{true: "available", false: "missing"}[ok]).Scan(&key, &match); err != nil || !match {
			t.Fatalf("native SRI parity: %v (%v)", err, match)
		}
		return key
	}
	add(1, 6)
	check(6, false)
	add(0, 0)
	original := check(7, true)
	exec("update metric_points set qty=1 where metric_name='sleep_total' and substring(date,1,10)=current_date::text")
	revised := check(7, true)
	if original == revised {
		t.Fatal("late SRI correction reused unchanged window")
	}
	var oldValue, newValue float64
	if err := db.pool.QueryRow(ctx, `select max(numeric_value) filter(where sample_key->>'capture'=$1),
		max(numeric_value) filter(where sample_key->>'capture'=$2) from tenant_native_metric_read_v1
		where metric_id='readiness.sleep_regularity_index'`, original, revised).Scan(&oldValue, &newValue); err != nil || oldValue != 100 || newValue >= oldValue {
		t.Fatalf("SRI revision %v -> %v: %v", oldValue, newValue, err)
	}
}
