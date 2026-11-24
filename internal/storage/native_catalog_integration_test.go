package storage

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"health-receiver/internal/testdb"
)

// Installs only the native serving view into a disposable, fully migrated schema.
func TestNativeCatalogDBTView(t *testing.T) {
	binary := os.Getenv("HEALTH_TEST_DBT")
	if binary == "" {
		t.Skip("set HEALTH_TEST_DBT to a dbt-postgres executable")
	}
	db, done := testDB(t)
	defer done()
	ctx := t.Context()
	if err := db.EnsureTenantIdentity(ctx, uuid.New(), uuid.New()); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := db.pool.QueryRow(ctx, "select current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.ParseConfig(testdb.DSN(t))
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	target := map[string]any{"type": "postgres", "host": conn.Host, "port": conn.Port, "user": conn.User,
		"password": "{{ env_var('DBT_ENV_SECRET_NATIVE_PASSWORD') }}", "dbname": conn.Database, "schema": schema, "threads": 1}
	profile := map[string]any{"health_dbt_metrics_pilot": map[string]any{"target": "test", "outputs": map[string]any{"test": target}}}
	b, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(temp, "profiles.yml"), b, 0600); err != nil {
		t.Fatal(err)
	}
	project, err := filepath.Abs("../../experiments/dbt_metrics")
	if err != nil {
		t.Fatal(err)
	}
	vars, err := exec.CommandContext(ctx, "python3", filepath.Join(project, "scripts/native_catalog_vars.py"), schema).Output()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, binary, "run", "--project-dir", project, "--profiles-dir", temp, "--target-path", filepath.Join(temp, "target"),
		"--log-path", filepath.Join(temp, "logs"), "--select", "metric_read_v1", "--vars", string(vars))
	cmd.Env = append(os.Environ(), "DBT_ENV_SECRET_NATIVE_PASSWORD="+conn.Password, "DBT_SEND_ANONYMOUS_USAGE_STATS=false")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dbt: %v\n%s", err, out)
	}
	read := func(id string) (CatalogMetricPage, error) {
		return db.ReadCatalogMetricPage(ctx, CatalogMetricRequest{MetricID: id, Limit: 1})
	}
	if _, err := read("step_count"); !errors.Is(err, ErrNativeCatalogUnavailable) {
		t.Fatalf("disabled reader: %v", err)
	}
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	db = &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	execSQL := func(sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	execSQL("insert into settings(key,value) values($1,'true') on conflict(key) do update set value='true'", SettingNativeCatalogReads)
	execSQL(`with receipt as (insert into health_records(automation_name,content_type,payload) values('synthetic','application/json','{}') returning id)
        insert into metric_points(metric_name,date,source,units,qty,health_record_id)
        select 'step_count','2026-11-01 02:00:00 +0200','Watch','count',0,id from receipt`)
	check := func(id, status string) {
		t.Helper()
		p, err := read(id)
		if err != nil || len(p.Samples) != 1 || p.Samples[0].Status != status {
			t.Fatalf("%s/%s: %v %v", id, status, p, err)
		}
	}
	check("step_count", "available")
	check("daily.steps", "no_row")
	execSQL("insert into sleep_goal values('2026-11-01',8,'test',now(),now())")
	check("sleep_goal.hours", "available")
	execSQL("update hdbt_state set shadow_enabled=true")
	execSQL("update hdbt_state set initialized=true,ready=true,formula_id=$1,source_generation=1", hdbtFormula)
	execSQL(`insert into settings(key,value) values('_cache_maintenance_v1','{"generation":1}') on conflict(key) do update set value=excluded.value`)
	execSQL("insert into hdbt_daily_metrics(date,steps) values('2026-11-01',321)")
	check("daily.steps", "available")
	execSQL("insert into hdbt_dirty(date,generation) values('2026-11-01',2)")
	check("daily.steps", "no_row")
	p, err := read("not_a_catalog_identifier")
	if err != nil || len(p.Samples) != 0 {
		t.Fatal("unsupported identifier advertised", err)
	}
	check("readiness_component.value", "no_row")
	execSQL(`insert into hdbt_decision_inputs(metric,decision_key,input_hash,formula_version,captured_at,inputs,result) values
        ('today_briefing','2026-01-01','old',2,now(),'{"raw":{"ReadinessEvidence":{"HRV":{"value":100}}}}','{}')`)
	check("readiness_component.value", "no_row")
	execSQL(`insert into hdbt_decision_inputs(metric,decision_key,input_hash,formula_version,captured_at,inputs,result) values
        ('today_briefing','2026-01-01','current',3,now(),'{"raw":{"ReadinessEvidence":{"HRV":{"present":false}}}}','{}')`)
	check("readiness_component.value", "missing")
	check("readiness_component.present", "available")
	execSQL("insert into settings(key,value) values($1,'true') on conflict(key) do update set value='true'", SettingDecisionResultReads)
	execSQL(`insert into hdbt_decision_inputs(metric,decision_key,input_hash,formula_version,captured_at,inputs,result) values
        ('today_decision','2026-01-01','decision',1,now(),'{}','{"mode":"recover","signal_keys":null}')`)
	check("today_decision.mode", "no_row")
	execSQL(`insert into hdbt_decision_results(metric,decision_key,input_hash,formula_version,result)
        select metric,decision_key,input_hash,formula_version+1,result from hdbt_decision_inputs where metric='today_decision'`)
	check("today_decision.mode", "no_row")
	execSQL("update hdbt_decision_results set formula_version=1 where metric='today_decision'")
	check("today_decision.mode", "available")
	check("today_decision.signal_keys", "missing")
	execSQL(`insert into workouts(external_id,name,start_time,end_time,duration_sec) values
        ('one','run','2026-10-21 08:00Z','2026-10-21 08:30Z',1800),
        ('two','run','2026-10-23 08:00Z','2026-10-23 08:30Z',1800)`)
	for id, want := range map[string]string{"workout_type.count": "2",
		"workout_type.first_seen": "2026-10-21T08:00:00Z", "workout_type.last_seen": "2026-10-23T08:00:00Z"} {
		page, err := read(id)
		if err != nil || len(page.Samples) != 1 {
			t.Fatalf("workout %s: %v %v", id, page, err)
		}
		got := page.Samples[0]
		if got.Number != nil && got.Number.String() == want {
			continue
		}
		if got.Text != nil {
			actual, err := time.Parse(time.RFC3339, *got.Text)
			if err == nil && actual.UTC().Format(time.RFC3339) == want {
				continue
			}
		}
		t.Fatalf("%s: got %s, want %s", id, got.Value, want)
	}
	for id, status := range map[string]string{"unclassified": "unclassified", "sleep_analysis": "input_contract", "athletic_readiness.target_wo_residual": "dormant"} {
		page, err := read(id)
		if err != nil || len(page.Samples) != 1 || page.Samples[0].Status != status || page.Samples[0].Reason == nil || page.Samples[0].Value != nil || page.Samples[0].Source != nil {
			t.Fatalf("%s disposition=%v err=%v", id, page, err)
		}
	}
	var count int
	if err := tx.QueryRow(ctx, "select count(distinct metric_id) from tenant_native_metric_read_v1").Scan(&count); err != nil || count != 456 {
		t.Fatalf("supported IDs=%d: %v", count, err)
	}
	check("readiness.readiness_display_score", "no_row")
	check("activity_section_status", "no_row")
	check("sleep_quality.confidence", "no_row")
	execSQL(`insert into hdbt_decision_inputs(metric,decision_key,input_hash,formula_version,captured_at,inputs,result) values
      ('today_briefing','2026-01-02','final',3,now(),'{
       "reader":"morning","headline":"","final_illness":"high",
       "effective":{"Date":"2026-01-02","HRV":[],"RHR":[],"Sleep":[],"HRVPresent":true,"RHRPresent":true,"SleepPresent":true,"LowQuality":false,"Provisional":false,"Samples":0,"Illness":""},
       "section_inputs_v1":{"Steps":[100,100,100],"Cal":null,"Exercise":[],"SpO2":[97,97,97],"VO2":[],"Resp":[]},
       "raw":{"ReadinessEvidence":{"SleepQuality":{"present":true,"freshness":"ok","confidence":"final"},
           "SleepDuration":{"present":true,"value":8},"SleepDeep":{"present":true,"value":1.4},
           "SleepREM":{"present":true,"value":2},"SleepAwake":{"present":true,"value":0}}}
      }','{}')`)
	for id, want := range map[string]string{"readiness.readiness_display_score": "45", "readiness.readiness_cap_reason": `"illness_suspicion_high"`,
		"activity_section_status": `"good"`, "cardio_section_status": `"good"`, "sleep_quality.score_pct": "100", "sleep_quality.confidence": `"final"`} {
		page, err := read(id)
		if err != nil || len(page.Samples) != 1 || string(page.Samples[0].Value) != want {
			t.Fatalf("replayed %s=%v want %s: %v", id, page, want, err)
		}
	}
	checkNativeWorkoutPeriod(t, db)
	checkNativeHRV(t, db)
	checkNativeNightInputs(t, db)
	checkNativeBalanceAndBaseline(t, db)
	checkNativeSleepReference(t, db)
	checkNativeSleepArchitecture(t, db)
	checkNativeEnergyReceipts(t, db)
	checkNativeChipReceipts(t, db)
	checkNativeFeatureReplay(t, db)
	checkNativeOxygenStress(t, db)
	// Even a second schema on a permissive test login cannot resolve the
	// first tenant's serving view through a fallback search_path entry.
	other := testdb.SchemaName("catalog_other")
	execSQL("create schema " + pgx.Identifier{other}.Sanitize())
	execSQL("set local search_path=" + pgx.Identifier{other}.Sanitize() + "," + pgx.Identifier{schema}.Sanitize())
	if _, err := read("step_count"); !errors.Is(err, ErrNativeCatalogUnavailable) {
		t.Fatalf("second schema resolved another profile's view: %v", err)
	}
}
