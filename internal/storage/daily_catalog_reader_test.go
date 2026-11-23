package storage

import (
	"context"
	"fmt"
	"testing"
)

func TestDailyCatalogWindowAndRollback(t *testing.T) {
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
	const previous, selected = "2026-05-08", "2026-05-09"
	exec(`insert into daily_scores(date,steps,hrv_avg,rhr_avg,vo2_avg,spo2_avg,exercise_min)
		values($1,30,45,70,55,94,20),($2,40,31,64,50,95,10)`, previous, selected)
	exec(`insert into hdbt_daily_metrics(date,steps,hrv_avg,rhr_avg,vo2_avg,spo2_avg,exercise_min)
		values($1,530,145,60,NULL,97,0),($2,540,131,65,51,98,11),('2026-05-07',900,82,72,42,94,38)`, previous, selected)
	// Give each remaining modeled field a different value in each source.
	exec(`update daily_scores set sleep_total=7,sleep_deep=1,sleep_rem=2,sleep_core=3,
		sleep_unspecified=0.5,sleep_awake=0.25,calories=350,resp_avg=14 where date=$1`, previous)
	exec(`update hdbt_daily_metrics set sleep_total=8,sleep_deep=2,sleep_rem=1,sleep_core=4,
		sleep_unspecified=0,sleep_awake=null,calories=351,resp_avg=15 where date=$1`, previous)
	state := emptyCacheMaintenanceState()
	state.Generation = 42
	setDailyStepSeriesJournal(t, unit, state)
	check := func(wantDBT bool) {
		t.Helper()
		got := unit.rawMetricsFromDailyScoresAt(selected, true)
		if got == nil || len(got.Daily) != 2 {
			t.Fatalf("calendar mismatch: %+v", got)
		}
		now, old := got.Daily[0], got.Daily[1]
		if now.Date != selected || old.Date != previous || *now.RHR != 64 || *now.HRV != 31 {
			t.Fatalf("anchor, calendar or other formula changed: %+v", got.Daily)
		}
		if wantDBT {
			const projection = `row(sleep_total,sleep_deep,sleep_rem,sleep_core,sleep_unspecified,
				sleep_awake,calories,resp_avg,hrv_avg,rhr_avg,spo2_avg,vo2_avg,steps,exercise_min)`
			query := fmt.Sprintf("select (select "+projection+" from %s p where p.date=$1) "+
				"is not distinct from (select "+projection+" from hdbt_daily_metrics where date=$1)", dailyCatalogSource)
			var match bool
			if err := tx.QueryRow(ctx, query, previous, selected).Scan(&match); err != nil || !match {
				t.Fatalf("completed-day field projection mismatch: match=%t err=%v", match, err)
			}

			if *old.Steps != 530 || *old.HRV != 145 || *old.RHR != 60 || *old.SpO2 != 97 || old.VO2 != nil || old.Exercise != nil {
				t.Fatalf("published fields differ: %+v", old)
			}
		} else if *old.Steps != 30 || *old.HRV != 45 || *old.RHR != 70 || *old.SpO2 != 94 || *old.VO2 != 55 || *old.Exercise != 20 {
			t.Fatalf("fallback fields differ: %+v", old)
		}
	}
	exec("update hdbt_state set shadow_enabled=true")
	exec("update hdbt_state set read_enabled=true,initialized=true,ready=true,formula_id=$1,source_generation=42", hdbtFormula)
	check(false)
	exec("insert into settings(key,value) values($1,'true')", SettingDailyCatalogReads)
	exec("update hdbt_state set read_enabled=false")
	check(true)
	exec("insert into hdbt_backfill(date) values($1)", previous)
	check(false)
	exec("delete from hdbt_backfill where date=$1", previous)
	exec("insert into hdbt_dirty(date,generation) values($1,42)", previous)
	check(false)
	exec("delete from hdbt_dirty where date=$1", previous)
	exec("update hdbt_state set source_generation=41")
	check(false)
	exec("update hdbt_state set source_generation=42,formula_id='unverified'")
	check(false)
	exec("update hdbt_state set formula_id=$1", hdbtFormula)
	check(true)
	exec("update settings set value='false' where key=$1", SettingDailyCatalogReads)
	check(false)
	exec("update settings set value='true' where key=$1", SettingDailyCatalogReads)
	exec("alter table hdbt_daily_metrics rename to missing_daily_publication")
	check(false)
	// A failing publication read inside an existing transaction must not
	// prevent the caller from using that transaction for its other sections.
	var ok bool
	if err := tx.QueryRow(ctx, "select true").Scan(&ok); err != nil || !ok {
		t.Fatalf("caller transaction lost: %v", err)
	}
}
