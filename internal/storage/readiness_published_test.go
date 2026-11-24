package storage

import (
	"encoding/json"
	"testing"
)

func TestReadinessPublicationMatchesExactInputAndCompleteRange(t *testing.T) {
	db, done := testDB(t)
	defer done()
	ctx := t.Context()
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	inputs := []readinessScoreInput{{Date: "2026-01-18"}, {Date: "2026-01-19"}}
	lookup := func(inputs []readinessScoreInput, want bool) {
		t.Helper()
		b, err := json.Marshal(inputs)
		if err != nil {
			t.Fatal(err)
		}
		results, found := unit.publishedReadinessScores(ctx, "history", b, len(inputs))
		if found != want || (found && (len(results) != len(inputs) || results[0].RawScore != 70)) {
			t.Fatalf("published lookup found=%v scores=%v", found, results)
		}
	}
	exec("update hdbt_state set shadow_enabled=true")
	exec("insert into settings(key,value) values($1,'true') on conflict(key) do update set value=excluded.value", SettingDecisionCapture)
	scores, err := unit.readReadinessScores(ctx, "history", inputs)
	if err != nil || len(scores) != 2 {
		t.Fatalf("initial capture %v: %v", scores, err)
	}
	exec("insert into hdbt_decision_results(metric,decision_key,input_hash,formula_version,result) select metric,decision_key,input_hash,formula_version,result from hdbt_decision_inputs where metric='readiness'")
	lookup(inputs, false)
	exec("insert into settings(key,value) values($1,'true') on conflict(key) do update set value=excluded.value", SettingDecisionResultReads)
	lookup(inputs, true)
	inputs[1].Sleep = []float64{5}
	lookup(inputs, false)
	scores, err = unit.readReadinessScores(ctx, "history", inputs)
	if err != nil || len(scores) != 2 {
		t.Fatalf("fallback for late sync %v: %v", scores, err)
	}
	exec("update hdbt_decision_results set formula_version=formula_version+1")
	lookup(inputs[:1], false)
}
