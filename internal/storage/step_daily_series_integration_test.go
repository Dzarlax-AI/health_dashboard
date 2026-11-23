package storage

import (
	"slices"
	"testing"

	"health-receiver/internal/testdb"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func setDailyStepSeriesJournal(t *testing.T, db *DB, state CacheMaintenanceState) {
	t.Helper()
	if _, err := db.withCacheMaintenanceTx(t.Context(), func(_ pgx.Tx, stored *CacheMaintenanceState) error {
		*stored = state
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func assertDailyStepSeries(t *testing.T, db *DB, from, to string, want ...DailyStepPoint) {
	t.Helper()
	got, err := db.DailyStepSeries(t.Context(), from, to)
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("DailyStepSeries(%q,%q)=%+v, %v; want %+v", from, to, got, err, want)
	}
}

func TestDailyStepSeriesUsesPublishedPrecisionAndOnlyRecomputesDirtyDates(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	const day1 = "2026-09-01"
	const day2 = "2026-09-02"
	const day3 = "2026-09-03"
	const day4 = "2026-09-04"
	const day5 = "2026-09-05"
	const day6 = "2026-09-06"
	const day7 = "2026-09-07"
	const day8 = "2026-09-08"
	ctx := t.Context()
	_, err := db.pool.Exec(ctx, `insert into daily_scores(date,steps) values
		('2026-09-08',10),('2026-09-07',80),('2026-09-06',15),('2026-09-05',99),
		('2026-09-04',16777217),('2026-09-03',0),('2026-09-02',null),('2026-09-01',20)`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := db.InsertRaw(Record{ContentType: "application/json; metrics-contract=2", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.pool.Exec(ctx, `insert into metric_points(health_record_id,metric_name,date,source,qty,quality,units)
		select $1,'step_count',d,'Watch',v,q,'count' from (values
		 ('2026-09-03 10:00:00 +0100',0,'ok'),
		 ('2026-09-04 10:00:00 +0100',16777216,'ok'),('2026-09-04 11:00:00 +0100',1,'ok'),
		 ('2026-09-05 10:00:00 +0100',99,'invalid'),('2026-09-06 10:00:00 +0100',35,'ok'),
		 ('2026-09-07 23:00:00 +0000',80,'ok'),('2026-09-08 00:00:00 +0100',80,'ok')
		) inputs(d,v,q)`, id)
	if err != nil {
		t.Fatal(err)
	}
	state := emptyCacheMaintenanceState()
	state.CompletedIdentity = CacheMaintenanceIdentity()
	setDailyStepSeriesJournal(t, db, state)
	assertDailyStepSeries(t, db, "", day6, DailyStepPoint{day1, 20}, DailyStepPoint{day3, 0}, DailyStepPoint{day4, 16777216}, DailyStepPoint{day5, 99}, DailyStepPoint{day6, 15})
	assertDailyStepSeries(t, db, day2, day2)
	assertDailyStepSeries(t, db, day6, day6, DailyStepPoint{day6, 15})

	// A completed identity can have outstanding foreground writes. Leave
	// day6 clean to distinguish its published 15 from its raw quantity 35.
	state.Generation = 1
	for _, day := range []string{day3, day4, day5, day7, day8} {
		state.Dirty[day] = 1
	}
	setDailyStepSeriesJournal(t, db, state)
	assertDailyStepSeries(t, db, "", day8, DailyStepPoint{day1, 20}, DailyStepPoint{day3, 0}, DailyStepPoint{day4, 16777216}, DailyStepPoint{day6, 15}, DailyStepPoint{day8, 80})
	assertDailyStepSeries(t, db, day5, day5)
	assertDailyStepSeries(t, db, day7, day7)
	assertDailyStepSeries(t, db, day8, day8, DailyStepPoint{day8, 80})

	assertFallback := func() {
		t.Helper()
		assertDailyStepSeries(t, db, "", day8, DailyStepPoint{day3, 0}, DailyStepPoint{day4, 16777216}, DailyStepPoint{day6, 35}, DailyStepPoint{day8, 80})
	}
	state = emptyCacheMaintenanceState()
	state.CompletedIdentity = "old-calculation"
	setDailyStepSeriesJournal(t, db, state)
	assertFallback()
	state.CompletedIdentity = CacheMaintenanceIdentity()
	state.TargetIdentity = CacheMaintenanceIdentity()
	state.Phase = CacheMaintenancePhaseAggregates
	state.FromDate, state.NextDate, state.EndDate = day1, day4, day8
	setDailyStepSeriesJournal(t, db, state)
	assertFallback()
	_, err = db.pool.Exec(ctx, `update settings set value='unrecognized' where key=$1`, cacheMaintenanceSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	assertFallback()
	_, err = db.pool.Exec(ctx, `delete from settings where key=$1`, cacheMaintenanceSettingKey)
	if err != nil {
		t.Fatal(err)
	}
	assertFallback()
}

func TestDailyStepSeriesCleanReadWithoutRawTableGrants(t *testing.T) {
	db, cleanup := testDB(t)
	defer cleanup()
	ctx := t.Context()
	var schema string
	var canCreateRole bool
	if err := db.pool.QueryRow(ctx, `select current_schema(),rolsuper or rolcreaterole from pg_roles where rolname=current_user`).Scan(&schema, &canCreateRole); err != nil {
		t.Fatal(err)
	}
	if !canCreateRole {
		t.Skip("disposable PostgreSQL account cannot create a restricted reader role")
	}
	state := emptyCacheMaintenanceState()
	state.CompletedIdentity = CacheMaintenanceIdentity()
	setDailyStepSeriesJournal(t, db, state)
	if _, err := db.pool.Exec(ctx, `insert into daily_scores(date,steps) values('2026-10-01',37)`); err != nil {
		t.Fatal(err)
	}
	roleName := testdb.SchemaName("step_qty_reader")
	role := pgx.Identifier{roleName}.Sanitize()
	schemaSQL := pgx.Identifier{schema}.Sanitize()
	if _, err := db.pool.Exec(ctx, "create role "+role+" nologin"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.pool.Exec(ctx, "revoke select on settings,daily_scores from "+role+"; revoke usage on schema "+schemaSQL+" from "+role+"; drop role "+role); err != nil {
			t.Errorf("remove scoped reader: %v", err)
		}
	}()
	if _, err := db.pool.Exec(ctx, "grant usage on schema "+schemaSQL+" to "+role+"; grant select on settings,daily_scores to "+role+"; grant "+role+" to current_user"); err != nil {
		t.Fatal(err)
	}
	cfg := db.pool.(*pgxpool.Pool).Config()
	cfg.ConnConfig.RuntimeParams["role"] = roleName
	restricted, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close()
	var rawAllowed bool
	if err = restricted.QueryRow(ctx, `select has_table_privilege('metric_points','select') or has_table_privilege('hourly_metrics','select') or has_table_privilege('health_records','select')`).Scan(&rawAllowed); err != nil || rawAllowed {
		t.Fatalf("raw read grants=%v err=%v", rawAllowed, err)
	}
	assertDailyStepSeries(t, &DB{pool: restricted}, "", "2026-10-01", DailyStepPoint{"2026-10-01", 37})
	state.Generation = 1
	state.Dirty["2026-10-05"] = 1
	setDailyStepSeriesJournal(t, db, state)
	assertDailyStepSeries(t, &DB{pool: restricted}, "2026-10-01", "2026-10-03", DailyStepPoint{"2026-10-01", 37})

}
