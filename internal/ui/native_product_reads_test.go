package ui

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"health-receiver/internal/storage"
)

// The worker harness provisions and installs the real dbt bundle in this isolated fixture.
func TestInstalledNativeProductReads(t *testing.T) {
	if os.Getenv("HDBT_INSTALLED_PRODUCT_FIXTURE") != "true" {
		t.Skip("requires the worker catalog fixture")
	}
	ctx := t.Context()
	pool, err := pgxpool.New(ctx, "host=/var/run/postgresql dbname=worker_catalog_fixture search_path=test_tenant,pg_catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := storage.NewFromPool(pool)
	exec := func(query string) {
		t.Helper()
		if _, err := pool.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into sleep_goal(effective_date,goal_hours,version,created_at,updated_at) select (date '2000-01-01'+i)::text,7.5,'manual',now(),now() from generate_series(0,9000)i;
 insert into workouts(external_id,name,start_time,end_time,duration_sec,location) select i::text,'Run',t,t,0,'outside' from generate_series(1,20000)i cross join lateral (select timestamptz '2000-01-01 UTC'+ i*interval '1 hour' t)s;
 insert into energy_snapshots(ts_bucket,date,bank,drain_delta,restore_delta,formula_version) select t,t::date::text,40,1,0,2 from generate_series(timestamptz '2000-01-01 UTC',timestamptz '2001-01-01 UTC',interval '5 minutes')t;
 analyze sleep_goal; analyze workouts; analyze energy_snapshots;`)
	goal := func(date string) string {
		t.Helper()
		r := requestWithTenant(http.MethodGet, "", db, "test_tenant")
		r.URL.RawQuery = "date=" + date
		w := httptest.NewRecorder()
		(&Handler{}).sleepGoal(w, r)
		if w.Code != 200 {
			t.Fatalf("HTTP %d", w.Code)
		}
		return w.Body.String()
	}
	original := goal("2001-01-01")
	absent := goal("1990-01-01")
	if !strings.Contains(original, "7.5") || strings.Contains(absent, "7.5") {
		t.Fatal("invalid baseline")
	}
	exec(`insert into settings(key,value) values('dbt_native_catalog_reads_v1','true')`)
	if goal("2001-01-01") != original || goal("1990-01-01") != absent {
		t.Fatal("sleep goal HTTP parity")
	}
	w, err := db.GetWorkout("123")
	if err != nil || w == nil || w.Location != "outside" {
		t.Fatal("archived workout", err)
	}
	energy, err := db.GetLatestEnergySnapshotForDate(ctx, "2000-06-01")
	if err != nil || energy == nil || energy.Bank != 40 {
		t.Fatal("archived energy", err)
	}
	for _, query := range []string{"select goal_hours from tenant_native_sleep_goal_v1 where effective_date<='2001-01-01' order by effective_date desc limit 1", "select name from tenant_native_workout_v1 where external_id='123'", "with day as materialized (select ts_bucket,bank from tenant_native_energy_snapshot_v1 where date='2000-06-01') select bank from day order by ts_bucket desc limit 1"} {
		rows, err := pool.Query(ctx, "explain (analyze,buffers) "+query)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan = append(plan, line)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		result := strings.Join(plan, "\n")
		if !strings.Contains(result, "Index") || strings.Contains(result, "Seq Scan") {
			t.Fatal(result)
		}
		if strings.Contains(query, "energy_snapshot") && !strings.Contains(result, "idx_energy_snapshots_date") {
			t.Fatal(result)
		}
		t.Log(result)
	}
	exec(`insert into settings(key,value) values('dbt_decision_capture_v1','true');
	with record as (insert into health_records(payload) values('{}') returning id)
	insert into metric_points(health_record_id,metric_name,date,qty,source,quality,received_at)
	select id,'sleep_core','2026-10-22 01:00:00 +0000',5,'Apple Watch','ok','2026-10-22 06:05 UTC' from record`)
	start := time.Date(2026, 10, 22, 7, 0, 0, 0, time.UTC)
	for i, want := range []string{"still_writing", "quiet_timeout", "still_writing", "quiet_timeout"} {
		if i == 2 {
			exec(`update metric_points set qty=5.1 where metric_name='sleep_core'`)
		}
		status, err := db.ComputeMorningWakeStatus("2026-10-22", time.UTC, start.Add(time.Duration(i)*30*time.Minute))
		if err != nil || status.Reason != want {
			t.Fatalf("wake %d = %v, %v", i, status, err)
		}
	}
	var revisions, matches, batches int
	if err := pool.QueryRow(ctx, `select (select count(*) from hdbt_decision_inputs where metric='wake_confirmation'),
	count(*) filter(where (c.value#>>'{}')::timestamptz=(r.result->>'StoredWake')::timestamptz),
	(select count(*) from hdbt_decision_inputs where metric='wake_segments') from tenant_native_metric_read_v1 c
	join hdbt_decision_inputs r on r.metric='wake_confirmation' and r.input_hash=c.sample_key->>'revision'
	where c.metric_id='wake_time' and c.source='wake_confirmation_v1'`).Scan(&revisions, &matches, &batches); err != nil || revisions != 4 || matches != 4 || batches != 2 {
		t.Fatalf("wake revisions=%d matches=%d segment batches=%d err=%v", revisions, matches, batches, err)
	}
	var exact bool
	if err := pool.QueryRow(ctx, `select exists(select from tenant_native_metric_read_v1 c join derived_metrics d
	on d.metric_name='wake_time' and d.metric_date='2026-10-22' and (c.value#>>'{}')::timestamptz=d.value_timestamp
	where c.metric_id='wake_time' and c.source='wake_confirmation_v1' and exists(
	select from hdbt_decision_inputs r where r.input_hash=c.sample_key->>'revision' and r.metric='wake_confirmation'
	  and (r.result->>'candidate_wake')::timestamptz=d.value_timestamp
	  and d.value_timestamp=timestamptz '2026-10-22 06:05:59.999656 UTC'))`).Scan(&exact); err != nil || !exact {
		t.Fatalf("fractional wake timestamp lost storage precision: %v", err)
	}
	// Canary values in the disposable projection prove that the real GET uses the selected relation.
	exec(`create or replace view tenant_native_sleep_goal_v1 with(security_invoker=true) as select effective_date,(goal_hours+1)::double precision goal_hours,version,updated_at from sleep_goal`)
	if !strings.Contains(goal("2001-01-01"), "8.5") {
		t.Fatal("endpoint did not select native view")
	}
	exec(`update settings set value='false' where key='dbt_native_catalog_reads_v1'`)
	if goal("2001-01-01") != original {
		t.Fatal("flag rollback changed response")
	}
}
