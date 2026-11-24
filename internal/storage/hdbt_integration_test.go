package storage

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"health-receiver/internal/testdb"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestHDBTMutationAndStepsCutover(t *testing.T) {
	db, done := testDB(t)
	defer done()
	ctx := t.Context()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	want := func(q string, n int) {
		t.Helper()
		var got int
		if err := db.pool.QueryRow(ctx, q).Scan(&got); err != nil || got != n {
			t.Fatalf("%s: got %d want %d: %v", q, got, n, err)
		}
	}
	const day = "2026-10-11"
	defer func() {
		exec("update hdbt_state set shadow_enabled=false; truncate hdbt_dirty,hdbt_daily_metrics,hdbt_step_repairs")
	}()
	state := emptyCacheMaintenanceState()
	state.CompletedIdentity = CacheMaintenanceIdentity()
	setDailyStepSeriesJournal(t, db, state)
	exec("insert into daily_scores(date,steps) values($1,41)", day)
	exec("insert into hdbt_daily_metrics(date,steps) values($1,72)", day)
	assertDailyStepSeries(t, db, "", day, DailyStepPoint{day, 41})
	if err := db.MarkCacheDirty(ctx, []string{day}); err != nil {
		t.Fatal(err)
	}
	want("select count(*) from hdbt_dirty", 0)
	exec("update hdbt_state set shadow_enabled=true")
	receipt, err := db.InsertRaw(Record{AutomationName: "Health dash - Hourly", ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	point := MetricPoint{"step_count", "count", day + " 08:00:00 +0000", 6, "Watch"}
	if err := db.InsertPoints(receipt, []MetricPoint{point}); err != nil {
		t.Fatal(err)
	}
	// Remove an accepted positive step through the normal source replacement.
	point.Qty = 0
	if err := db.InsertPoints(receipt, []MetricPoint{point}); err != nil {
		t.Fatal(err)
	}
	want("select count(*) from hdbt_dirty where generation=3", 1)
	want("select count(*) from hdbt_step_repairs where generation=3", 1)
	observed, err := db.CaptureCacheDirty(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteCacheDirty(ctx, observed); err != nil {
		t.Fatal(err)
	}
	want("select count(*) from hdbt_dirty where generation=3", 1)
	// A failed source transaction publishes neither dates nor repair evidence.
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if err = repairWithdrawnSteps(ctx, tx, json.RawMessage(`[["2026-10-12 08:12","Watch"]]`)); err != nil {
		t.Fatal(err)
	}
	if err = db.MarkCacheDirtyTx(ctx, tx, []string{"2026-10-12"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	want("select count(*) from hdbt_dirty where generation=4", 0)
	want("select count(*) from hdbt_step_repairs where generation=4", 0)
	exec("update daily_scores set steps=41 where date=$1", day)
	exec("update hdbt_state set ready=true,initialized=true,read_enabled=true,formula_id=$1,source_generation=3", hdbtFormula)
	assertDailyStepSeries(t, db, "", day, DailyStepPoint{day, 0})
	exec("delete from hdbt_dirty")
	assertDailyStepSeries(t, db, "", day, DailyStepPoint{day, 72})
	assertDailyStepSeries(t, db, day, day, DailyStepPoint{day, 72})
	exec("update hdbt_state set source_generation=2")
	assertDailyStepSeries(t, db, "", day, DailyStepPoint{day, 0})
	exec("update hdbt_state set source_generation=3, formula_id='old'")
	assertDailyStepSeries(t, db, "", day, DailyStepPoint{day, 0})
	exec("update hdbt_state set formula_id=$1, read_enabled=false", hdbtFormula)
	assertDailyStepSeries(t, db, "", day, DailyStepPoint{day, 41})
	exec("update hdbt_state set shadow_enabled=false")
	want("select count(*) from hdbt_state where ready or initialized or read_enabled", 0)
}

func TestDecisionQueueMigrationAndAtomicCapture(t *testing.T) {
	db, done := testDB(t)
	defer done()
	ctx := t.Context()
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	exec := func(q string) {
		t.Helper()
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	count := func(want int) {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, "select count(*) from hdbt_decision_queue").Scan(&n); err != nil || n != want {
			t.Fatalf("queue count %d, want %d: %v", n, want, err)
		}
	}
	exec("alter table hdbt_decision_inputs disable trigger hdbt_enqueue_decision")
	exec(`insert into hdbt_decision_inputs select 'readiness','history/2026-01-18',n::text,now(),
		case when n=1 then 4 else 3 end,'{}','{}' from generate_series(1,3) n`)
	exec(`insert into hdbt_decision_results select metric,decision_key,input_hash,formula_version,result
		from hdbt_decision_inputs where input_hash='3'`)
	count(0)
	exec(decisionJournalDDL)
	count(1)
	exec(decisionJournalDDL)
	count(1)
	exec("savepoint before_capture")
	exec(`insert into hdbt_decision_inputs values('today_decision','2026-01-19','4',now(),1,'{}','{}')`)
	count(2)
	exec("rollback to savepoint before_capture")
	count(1)
}

func TestHDBTFlagsAreOfflineContractObjects(t *testing.T) {
	db, done := testDB(t)
	defer done()
	ctx := t.Context()
	// The transaction simulates a v16 tenant without altering the shared test schema.
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "drop table hdbt_state"); err != nil {
		t.Fatal(err)
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	if err := unit.MarkCacheDirty(ctx, []string{"2026-10-11"}); err != nil {
		t.Fatal(err)
	}
	if err := unit.MigrateSchemaContractContext(ctx); err != nil {
		t.Fatal(err)
	}
	var flags bool
	if err := tx.QueryRow(ctx, "select shadow_enabled or read_enabled from hdbt_state where singleton").Scan(&flags); err != nil || flags {
		t.Fatalf("initial flags: %t %v", flags, err)
	}
	for _, definition := range []string{
		"update of read_enabled on hdbt_state for each row when (old.shadow_enabled is distinct from new.shadow_enabled)",
		"update of shadow_enabled on hdbt_state for each row when (false)",
	} {
		if _, err := tx.Exec(ctx, "create or replace trigger hdbt_enrollment_change before "+definition+" execute function hdbt_reset_enrollment()"); err != nil {
			t.Fatal(err)
		}
		if err := unit.verifyHDBTEnrollmentTrigger(ctx); err == nil {
			t.Errorf("accepted invalid enrollment trigger: %s", definition)
		}
	}
	if _, err := tx.Exec(ctx, "alter table hdbt_state disable trigger hdbt_enrollment_change"); err != nil {
		t.Fatal(err)
	}
	if err := unit.VerifySchemaContractContext(ctx); err == nil {
		t.Fatal("disabled enrollment fence accepted")
	}
}

func TestHDBTWithdrawalUpgradePreservesQueuedDecisions(t *testing.T) {
	db, done := testDB(t)
	defer done()
	ctx := t.Context()
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, `drop table hdbt_i_daily_withdrawals,hdbt_daily_withdrawals;
		alter table hdbt_decision_queue drop column blocked_reason;
		insert into hdbt_decision_inputs(metric,decision_key,input_hash,captured_at,formula_version,inputs,result)
		values('readiness','history/2026-10-10','fixture',now(),3,'{}','{}')`); err != nil {
		t.Fatal(err)
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	for range 2 {
		if err := unit.MigrateSchemaContractContext(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var pending int
	if err := tx.QueryRow(ctx, `select count(*) from hdbt_decision_queue
		where input_hash='fixture' and blocked_reason is null`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("migration lost or duplicated queued decision: pending=%d err=%v", pending, err)
	}
	if err := unit.VerifySchemaContractContext(ctx); err != nil {
		t.Fatal(err)
	}
}

// Exercise PostgreSQL's actual privilege checks, including an explicit target
// schema. Roll back the fixture role and sibling schema together with its data.
func TestHDBTStepsRoleIsolation(t *testing.T) {
	db, done := testDB(t)
	defer done()
	ctx := t.Context()
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var schema string
	var createRole bool
	if err := tx.QueryRow(ctx, "select current_schema(),rolcreaterole or rolsuper from pg_roles where rolname=current_user").Scan(&schema, &createRole); err != nil {
		t.Fatal(err)
	}
	if !createRole {
		t.Skip("requires a disposable role administrator")
	}
	role := pgx.Identifier{testdb.SchemaName("hdbt_reader")}.Sanitize()
	neighbor := pgx.Identifier{testdb.SchemaName("hdbt_neighbor")}.Sanitize()
	local := pgx.Identifier{schema}.Sanitize()
	must("create role " + role + " nologin")
	must("grant " + role + " to current_user")
	must("create schema " + neighbor)
	must("create table " + neighbor + ".hdbt_daily_metrics(date text primary key, steps real)")
	must("grant usage on schema " + local + " to " + role)
	must("grant select on settings,hdbt_state,hdbt_dirty,hdbt_daily_metrics,hdbt_backfill to " + role)
	must("grant select on daily_scores,hourly_metrics,metric_points,health_records to " + role)
	state := emptyCacheMaintenanceState()
	state.CompletedIdentity = CacheMaintenanceIdentity()
	if _, err := lockCacheMaintenanceRow(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := saveCacheMaintenanceRow(ctx, tx, state); err != nil {
		t.Fatal(err)
	}
	must("update hdbt_state set shadow_enabled=true")
	must("update hdbt_state set initialized=true,ready=true,read_enabled=true,source_generation=0,formula_id=$1", hdbtFormula)
	must("insert into hdbt_daily_metrics(date,steps) values('2026-10-10',72)")
	must("insert into daily_scores(date,steps) values('2026-10-07',19),('2026-10-10',null)")
	must("set local role " + role)
	generation := uint64(0)
	read, ok, readErr := readHDBTSteps(ctx, tx, "", "2026-10-10", &generation)
	if readErr != nil || !ok || len(read) != 1 || read[0] != (DailyStepPoint{"2026-10-10", 72}) {
		t.Fatal("tenant-scoped published series unavailable")
	}
	for _, statement := range []string{"select steps from " + neighbor + ".hdbt_daily_metrics", "insert into " + neighbor + ".hdbt_daily_metrics values('2026-10-10',42)"} {
		err := pgx.BeginFunc(ctx, tx, func(probe pgx.Tx) error { _, err := probe.Exec(ctx, statement); return err })
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("expected isolation denial: %v", err)
		}
	}
	must("reset role")
	must("revoke select on hdbt_daily_metrics from " + role)
	must("update daily_scores set steps=72 where date='2026-10-10'")
	state.Generation = 1
	if err := saveCacheMaintenanceRow(ctx, tx, state); err != nil {
		t.Fatal(err)
	}
	must("update hdbt_state set source_generation=1")
	must("insert into hdbt_dirty(date,generation) values('2026-10-10',1)")
	must("set local role " + role)
	read, readErr = db.dailyStepSeriesTx(ctx, tx, "", "2026-10-10")
	if readErr != nil || !slices.Equal(read, []DailyStepPoint{{"2026-10-07", 19}}) {
		t.Fatalf("Go fallback should retain historical data without reusing the withdrawn publication: %+v: %v", read, readErr)
	}
}

func TestHDBTLiveDailyWithdrawalRequiresAcceptedReplacement(t *testing.T) {
	db, done := testDB(t)
	defer done()
	const day = "2026-10-10"
	const stamp = day + " 08:00:00 +0000"
	insert := func(hrv, sleep float64) {
		t.Helper()
		record, err := db.InsertRaw(Record{ContentType: "application/json", Payload: "{}"})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.InsertPoints(record, []MetricPoint{
			{MetricName: "heart_rate_variability", Units: "ms", Date: stamp, Qty: hrv, Source: "Watch"},
			{MetricName: "sleep_total", Units: "hr", Date: stamp, Qty: sleep, Source: "Watch"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	insert(42, 7)
	insert(0, 0)
	if err := db.UpsertRecentCache([]string{day}, false); err != nil {
		t.Fatal(err)
	}
	var metrics []string
	if err := db.pool.QueryRow(t.Context(), `select array_agg(metric_name order by metric_name)
		from hdbt_daily_withdrawals where date=$1`, day).Scan(&metrics); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(metrics, []string{"heart_rate_variability"}) {
		t.Fatalf("durable withdrawals after Go repair = %v; protected sleep must not be withdrawn", metrics)
	}
}

func TestHDBTStepsDirtyNeighborAndPartialBootstrap(t *testing.T) {
	db, done := testDB(t)
	defer done()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	defer exec("update hdbt_state set shadow_enabled=false; truncate hdbt_dirty,hdbt_backfill,hdbt_daily_metrics,hdbt_step_state")
	state := emptyCacheMaintenanceState()
	state.CompletedIdentity = CacheMaintenanceIdentity()
	state.Generation = 4
	setDailyStepSeriesJournal(t, db, state)
	const priorDay = "2026-05-01"
	const changedDay = "2026-05-12"
	// Three minute maxima match the historical REAL only in chronological
	// order. Proving that cell admits the second archive sample in minute zero.
	receipt, err := db.InsertRaw(Record{ContentType: "apple-health-import", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	exec(`insert into metric_points(health_record_id,metric_name,units,date,source,qty,origin)
		select $1,'step_count','count','2026-05-12 09:'||stamp||' +0000','Apple Watch',qty,'apple_health_xml'
		from (values ('02:01',7.15::real),('01:01',0.15::real),('00:02',6::real),('00:01',11.1::real)) v(stamp,qty)`, receipt)
	exec(`insert into hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count)
		values ('step_count','2026-05-12 09:00','Apple Watch',18.4,0.15,11.1,3),
		       ('step_count','2026-05-12 10:00','iPhone',80,40,40,2)`)
	exec(`insert into hdbt_step_state(hour,source,avg_val,min_val,max_val,sample_count,withdrawn)
		select hour,source,avg_val,min_val,max_val,sample_count,false from hourly_metrics where metric_name='step_count'`)
	exec(`insert into daily_scores(date,steps) values('2026-05-01',18),('2026-05-12',99)`)
	exec(`insert into hdbt_daily_metrics(date,steps) values('2026-05-01',21),('2026-05-12',57)`)
	exec("update hdbt_state set shadow_enabled=true")
	exec("update hdbt_state set initialized=true,read_enabled=true,ready=false,formula_id=$1,source_generation=3", hdbtFormula)
	exec("insert into hdbt_dirty(date,generation) values('2026-05-10',4)")
	expected := DailyStepPoint{changedDay, float64(float32(104.4))}
	assertDailyStepSeries(t, db, "", changedDay, DailyStepPoint{priorDay, 21}, expected)
	assertDailyStepSeries(t, db, changedDay, changedDay, expected)
	assertDailyStepSeries(t, db, priorDay, priorDay, DailyStepPoint{priorDay, 21})
	// An incomplete bootstrap cannot reuse the partial published series.
	exec("insert into hdbt_backfill(date) values('2026-05-15')")
	assertDailyStepSeries(t, db, "", changedDay, DailyStepPoint{priorDay, 18}, expected)
	assertDailyStepSeries(t, db, changedDay, changedDay, expected)
	exec("update hdbt_state set read_enabled=false")
	assertDailyStepSeries(t, db, "", changedDay, DailyStepPoint{priorDay, 18}, DailyStepPoint{changedDay, 99})
}

func TestHDBTStepsLiveDeliveryWithIncompleteHour(t *testing.T) {
	db, done := testDB(t)
	defer done()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	defer exec("update hdbt_state set shadow_enabled=false; truncate hdbt_dirty,hdbt_backfill,hdbt_daily_metrics,hdbt_step_repairs,hdbt_step_state")
	state := emptyCacheMaintenanceState()
	state.CompletedIdentity = CacheMaintenanceIdentity()
	setDailyStepSeriesJournal(t, db, state)
	const day = "2026-05-06"
	// The old iPhone cell survived after its two original readings went missing.
	const hour = day + " 08:00"
	exec("insert into hourly_metrics(metric_name,hour,source,avg_val,min_val,max_val,sample_count) values('step_count',$1,'iPhone',80,40,40,2)", hour)
	exec("insert into hdbt_step_state(hour,source,avg_val,min_val,max_val,sample_count,withdrawn) values($1,'iPhone',80,40,40,2,false)", hour)
	exec("insert into hdbt_daily_metrics(date,steps) values($1,80)", day)
	exec("update hdbt_state set shadow_enabled=true")
	exec("update hdbt_state set initialized=true,read_enabled=true,ready=true,formula_id=$1,source_generation=0", hdbtFormula)
	receipt, err := db.InsertRaw(Record{AutomationName: "Health dash - Hourly", ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	point := MetricPoint{"step_count", "count", day + " 08:00:00 +0000", 5, "Apple Watch"}
	if err := db.InsertPoints(receipt, []MetricPoint{point}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertRecentCache([]string{day}, false); err != nil {
		t.Fatal(err)
	}
	assertDailyStepSeries(t, db, "", day, DailyStepPoint{day, 80})
	// A previous dbt publication can outlive even the ordinary Go cell.
	exec("delete from hourly_metrics where metric_name='step_count' and hour=$1", hour)
	assertDailyStepSeries(t, db, "", day, DailyStepPoint{day, 80})
	// Revoke the newer source through the same cleanup used by the importer.
	// Its receipt covers Watch only. Missing iPhone samples remain protected.
	db.RemoveAutoExportForRange(day, day)
	var points, repairs int
	if err := db.pool.QueryRow(t.Context(), `select (select count(*) from metric_points where source='Apple Watch'),
		(select count(*) from hdbt_step_repairs where source='Apple Watch')`).Scan(&points, &repairs); err != nil || points != 0 || repairs != 1 {
		t.Fatalf("points=%d repairs=%d: %v", points, repairs, err)
	}
	assertDailyStepSeries(t, db, "", day, DailyStepPoint{day, 80})
}

func TestHDBTDirtyAliasOutsideRangeWithGermanDateStyle(t *testing.T) {
	db, done := testDB(t)
	defer done()
	ctx := t.Context()
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("set local datestyle='German, DMY'")
	exec("update hdbt_state set shadow_enabled=true")
	exec("update hdbt_state set initialized=true,read_enabled=true,ready=true,formula_id=$1,source_generation=0", hdbtFormula)
	state := emptyCacheMaintenanceState()
	state.Generation = 1
	state.CompletedIdentity = CacheMaintenanceIdentity()
	if _, err = lockCacheMaintenanceRow(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err = saveCacheMaintenanceRow(ctx, tx, state); err != nil {
		t.Fatal(err)
	}
	id, err := unit.InsertRaw(Record{ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	exec(`insert into metric_points(health_record_id,metric_name,units,date,source,qty) values
	  ($1,'step_count','count','2026-05-12 23:30:00 +0000','Watch',30),
	  ($1,'step_count','count','2026-05-13 00:30:00 +0100','Watch',30)`, id)
	exec("insert into hdbt_daily_metrics(date,steps) values('2026-05-12',30)")
	exec("insert into hdbt_dirty(date,generation) values('2026-05-13',1)")
	generation := uint64(1)
	for _, tc := range []struct {
		through string
		want    []DailyStepPoint
	}{
		{"2026-05-12", nil}, {"2026-05-13", []DailyStepPoint{{"2026-05-13", 30}}},
	} {
		got, enabled, err := readHDBTSteps(ctx, tx, "", tc.through, &generation)
		if err != nil || !enabled || !slices.Equal(got, tc.want) {
			t.Fatalf("dirty alias through %s: got %+v enabled=%v: %v", tc.through, got, enabled, err)
		}
	}
	// Formatting a BC neighbor without its era could incorrectly read December
	// of year 1 AD even though the chart ends on January 1.
	exec("insert into hdbt_dirty(date,generation) values('0001-01-01',1)")
	exec("insert into metric_points(health_record_id,metric_name,units,date,source,qty) values($1,'step_count','count','0001-12-31 10:00:00 +0000','Watch',9)", id)
	got, enabled, err := readHDBTSteps(ctx, tx, "", "0001-01-01", &generation)
	if err != nil || !enabled || len(got) != 0 {
		t.Fatalf("steps outside earliest day: %+v enabled=%v: %v", got, enabled, err)
	}

}

func TestHDBTStepsRetainedPreimageAcceptsMiddleMinute(t *testing.T) {
	db, done := testDB(t)
	defer done()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(t.Context(), q, args...); err != nil {
			t.Fatal(err)
		}
	}
	defer exec("update hdbt_state set shadow_enabled=false; truncate hdbt_dirty,hdbt_step_inputs,hdbt_step_state,hdbt_daily_metrics,hdbt_step_repairs")
	state := emptyCacheMaintenanceState()
	state.CompletedIdentity = CacheMaintenanceIdentity()
	setDailyStepSeriesJournal(t, db, state)
	const day = "2026-04-06"
	id, err := db.InsertRaw(Record{ContentType: "application/json", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	exec(`insert into metric_points(health_record_id,metric_name,units,date,source,qty) values
		($1,'step_count','count','2026-04-06 08:00:00 +0000','Watch',4),
		($1,'step_count','count','2026-04-06 08:02:00 +0000','Watch',8)`, id)
	exec(`insert into hdbt_step_inputs select metric_name,date,source,qty,quality,health_record_id,origin,units,id,received_at,source_snapshot_at from metric_points`)
	exec("insert into hdbt_step_state(hour,source,avg_val,min_val,max_val,sample_count,withdrawn) values('2026-04-06 08:00','Watch',12,4,8,2,false)")
	exec("insert into hdbt_daily_metrics(date,steps) values($1,12)", day)
	exec("update hdbt_state set shadow_enabled=true")
	exec("update hdbt_state set initialized=true,read_enabled=true,ready=true,formula_id=$1,source_generation=0", hdbtFormula)
	if err = db.InsertPoints(id, []MetricPoint{{"step_count", "count", day + " 08:01:00 +0000", 1, "Watch"}}); err != nil {
		t.Fatal(err)
	}
	assertDailyStepSeries(t, db, "", day, DailyStepPoint{day, 13})
}
