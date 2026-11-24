package storage

import (
	"context"
	"testing"
)

func TestEnergyPublishedDecisionRequiresCurrentInputs(t *testing.T) {
	db, done := testDB(t)
	defer done()
	ctx := context.Background()
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	exec := func(sql string) {
		t.Helper()
		if _, err := unit.pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	exec(hdbtDDL)
	exec(decisionJournalDDL)
	exec(`update hdbt_state set shadow_enabled=true;
 insert into settings(key,value) values('dbt_decision_capture_v1','true'),('dbt_decision_result_reads_v1','true') on conflict(key) do update set value='true'`)
	cfg := DefaultEnergyConfig()
	const day = "2026-01-22"
	read := func() (BankResult, energyTrace) {
		t.Helper()
		b, tr, err := unit.readEnergyBankSQL(ctx, day, cfg, nil)
		if err != nil {
			t.Fatal(err)
		}
		return b, tr
	}
	first, _ := read()
	// A distinguishable result proves which reader supplied the answer.
	exec(`insert into hdbt_decision_results select metric,decision_key,input_hash,formula_version,
 result||'{"Bank":17,"Trace":[1,2,3,4,5]}'::jsonb from hdbt_decision_inputs where metric='energy_bank'`)
	b, tr := read()
	if b.Bank != 17 || tr.beforeClamp != 5 {
		t.Fatalf("published Energy result missing: %v %v", b.Bank, tr)
	}
	cfg.AlphaFactor += 0.1
	b, _ = read()
	if b.Bank != first.Bank {
		t.Fatal("changed coefficients reused the previous publication")
	}
	cfg = DefaultEnergyConfig()
	exec(`insert into daily_scores(date,calories) values('2026-01-02',400)`)
	b, _ = read()
	if b.Bank == 17 {
		t.Fatal("late change on the first day of the 21-day window reused the previous result")
	}
	exec(`delete from daily_scores where date='2026-01-02'; update settings set value='false' where key='dbt_decision_result_reads_v1'`)
	b, _ = read()
	if b.Bank != first.Bank {
		t.Fatal("rollback did not restore direct computation")
	}
}
