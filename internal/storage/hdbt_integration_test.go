package storage

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"health-receiver/internal/testdb"
	"testing"
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
	assertDailyStepSeries(t, db, day, day, DailyStepPoint{day, 41})
	exec("update hdbt_state set source_generation=2")
	assertDailyStepSeries(t, db, "", day, DailyStepPoint{day, 0})
	exec("update hdbt_state set source_generation=3, formula_id='old'")
	assertDailyStepSeries(t, db, "", day, DailyStepPoint{day, 0})
	exec("update hdbt_state set formula_id=$1, read_enabled=false", hdbtFormula)
	assertDailyStepSeries(t, db, "", day, DailyStepPoint{day, 41})
	exec("update hdbt_state set shadow_enabled=false")
	want("select count(*) from hdbt_state where ready or initialized or read_enabled", 0)
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
	if _, err := tx.Exec(ctx, "alter table hdbt_state disable trigger hdbt_enrollment_change"); err != nil {
		t.Fatal(err)
	}
	if err := unit.VerifySchemaContractContext(ctx); err == nil {
		t.Fatal("disabled enrollment fence accepted")
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
	must("set local role " + role)
	generation := uint64(0)
	read, ok, readErr := readHDBTSteps(ctx, tx, "2026-10-10", &generation)
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
}
