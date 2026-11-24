package storage

import (
	"testing"

	"health-receiver/internal/health"
)

func TestTodayDecisionPublicationExactInputs(t *testing.T) {
	db, done := testDB(t)
	defer done()
	ctx := t.Context()
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	exec := func(stmt string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("update hdbt_state set shadow_enabled=true")
	setFlag := func(key, value string) {
		exec("insert into settings(key,value) values($1,$2) on conflict(key) do update set value=excluded.value", key, value)
	}
	setFlag(SettingDecisionCapture, "true")
	brief := &health.BriefingResponse{Date: "2026-01-18", ReadinessToday: 55}
	check := func(want string) {
		t.Helper()
		got, err := unit.liveDailyDecision(ctx, "2026-01-18", brief)
		if err != nil || got == nil || got.Mode != want {
			t.Fatalf("wanted %q, got %+v, error %v", want, got, err)
		}
	}
	check("active_recovery")
	// A distinctive synthetic result proves that the serving path consumed the publication.
	exec(`insert into hdbt_decision_results select metric,decision_key,input_hash,formula_version,
		result || '{"mode":"rest"}'::jsonb from hdbt_decision_inputs where metric='today_decision'`)
	setFlag(SettingDecisionResultReads, "true")
	check("rest")
	historical, err := unit.dailyDecision(ctx, brief)
	if err != nil || historical == nil || historical.Mode != "rest" {
		t.Fatalf("historical exact decision %+v, error %v", historical, err)
	}
	setFlag(SettingDecisionCapture, "false")
	check("rest")
	brief.ReadinessToday = 85
	check("moderate")
	brief.ReadinessToday = 55
	setFlag(SettingDecisionResultReads, "false")
	check("active_recovery")
}
