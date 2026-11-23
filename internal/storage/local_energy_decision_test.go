package storage

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"health-receiver/internal/health"
)

func TestInstalledLocalEnergyDecision(t *testing.T) {
	graph, energy := os.Getenv("PROFILE_GRAPH_SCHEMA"), os.Getenv("PROFILE_ENERGY_SCHEMA")
	if graph == "" || energy == "" {
		t.Skip("requires an isolated local dbt Energy graph")
	}
	db, err := NewLocalProfileGraphReader(t.Context(), os.Getenv("DATABASE_URL"), os.Getenv("PROFILE_SOURCE_SCHEMA"), graph)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	first, err := db.CaptureLocalEnergyDecision(t.Context(), "UTC", "2026-01-18")
	if err != nil {
		t.Fatal(err)
	}
	if err = db.VerifyLocalEnergyDecision(t.Context(), energy, first); err != nil {
		t.Fatal(err)
	}
	var frozen struct {
		Days       []json.RawMessage
		CapturedAt string `json:"captured_at"`
	}
	if err = json.Unmarshal(first.Inputs, &frozen); err != nil || len(frozen.Days) == 0 || frozen.CapturedAt == "" {
		t.Fatal("expected dated source rows with statement-time observation")
	}
	// A later projected sleep decision changes all selected dates. The original
	// packet must still reproduce independently of this second calculation.
	sleep, activity := 8.0, 500.0
	days := map[string]dailyInputs{}
	for i := 0; i < energyWindowDays; i++ {
		days[subtractDays("2026-01-18", i)] = dailyInputs{SleepTotal: &sleep, ActiveKcal: &activity}
	}
	var later LocalEnergyDecision
	ctx := context.WithValue(t.Context(), localEnergyCaptureKey{}, &later)
	_, _, err = db.readEnergyBankSQL(ctx, "2026-01-18", db.GetEnergyConfig(), days)
	if err != nil {
		t.Fatal(err)
	}
	if later.Result.State == first.Result.State && later.Result.Bank == first.Result.Bank {
		t.Fatal("late input did not change result")
	}
	if err = db.VerifyLocalEnergyDecision(t.Context(), energy, later); err != nil {
		t.Fatal(err)
	}
	if err = db.VerifyLocalEnergyDecision(t.Context(), energy, first); err != nil {
		t.Fatal(err)
	}
	wrong := first
	wrong.Result.Bank++
	if err = db.VerifyLocalEnergyDecision(t.Context(), energy, wrong); err == nil {
		t.Fatal("accepted an altered output")
	}
}

func TestInstalledLocalReadinessDecision(t *testing.T) {
	graph := os.Getenv("PROFILE_GRAPH_SCHEMA")
	if graph == "" {
		t.Skip("requires an isolated local dbt graph")
	}
	db, err := NewLocalProfileGraphReader(t.Context(), os.Getenv("DATABASE_URL"), os.Getenv("PROFILE_SOURCE_SCHEMA"), graph)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, reader := range []string{"history", "morning"} {
		packet, err := db.CaptureLocalReadinessDecision(t.Context(), "2026-01-01", reader)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.VerifyLocalReadinessDecision(t.Context(), packet); err != nil {
			t.Fatal(err)
		}
		altered := packet
		altered.Result.RawScore++
		if err = db.VerifyLocalReadinessDecision(t.Context(), altered); err == nil {
			t.Fatal("accepted altered Readiness")
		}
	}
}

// TestInstalledDecisionJournal activates the ordinary product DB on one
// disposable profile. A rollback prevents changes from affecting other probes.
func TestInstalledDecisionJournal(t *testing.T) {
	source, graph := os.Getenv("PROFILE_SOURCE_SCHEMA"), os.Getenv("PROFILE_GRAPH_SCHEMA")
	if graph == "" || os.Getenv("HEALTH_PROFILE_GRAPH_PROBE") != "1" {
		t.Skip("requires the disposable journal-enabled graph")
	}
	ctx := t.Context()
	scoped, err := NewLocalProfileGraphReader(ctx, os.Getenv("DATABASE_URL"), source, graph)
	if err != nil {
		t.Fatal(err)
	}
	scoped.Close()
	db, err := NewWithSchema(ctx, os.Getenv("DATABASE_URL"), source)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	input := readinessScoreInput{Date: "2026-01-18", HRV: []float64{45, 45, 45, 45, 45, 45, 45, 25, 60}, HRVPresent: true}
	run := func() {
		t.Helper()
		if _, err := unit.readReadinessScores(ctx, "morning", []readinessScoreInput{input}); err != nil {
			t.Fatal(err)
		}
		if _, err := unit.ComputeBankForDate(ctx, "UTC", input.Date); err != nil {
			t.Fatal(err)
		}
	}
	count := func(want int) {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, "select count(*) from hdbt_decision_inputs").Scan(&n); err != nil || n != want {
			t.Fatalf("receipt count %d want %d, error %v", n, want, err)
		}
	}
	run()
	count(0)
	if _, err = tx.Exec(ctx, "insert into settings(key,value) values($1,'true') on conflict(key) do update set value=excluded.value", SettingDecisionCapture); err != nil {
		t.Fatal(err)
	}
	run()
	count(2)
	run()
	count(2)
	input.HRV[0] = 27
	run()
	count(3)
	if _, err = tx.Exec(ctx, "insert into settings(key,value) values($1,'false') on conflict(key) do update set value=excluded.value", SettingDecisionCapture); err != nil {
		t.Fatal(err)
	}
	input.HRV[0] = 29
	run()
	count(3)
	// Verify every committed-shaped receipt, including the previous score.
	var energyMatches, readinessMatches int
	err = tx.QueryRow(ctx, `select count(*) from hdbt_decision_inputs j join `+pgx.Identifier{graph, "semantic_readiness_score_v1"}.Sanitize()+` r
        on j.metric='readiness' and r.profile=current_user and r.case_id='journal:'||j.input_hash
        where j.captured_at is not null and r.display_score is null and r.confidence is null and r.band is null
        and r.cap_reason is null and r.recovery_source is null
        and j.result=jsonb_build_object('RawScore',r.raw_score,'DisplayScore',r.solver_display_score,
            'Confidence',coalesce(r.solver_confidence,''),'CapReason',coalesce(r.solver_cap_reason,''))`).Scan(&readinessMatches)
	if err != nil || readinessMatches != 2 {
		t.Fatalf("readiness matches %d, error %v", readinessMatches, err)
	}
	err = tx.QueryRow(ctx, `select count(*) from hdbt_decision_inputs j join `+pgx.Identifier{graph, "semantic_energy_bank_v3"}.Sanitize()+` e
        on j.metric='energy_bank' and e.profile=current_user and e.capture_id='journal:'||j.input_hash
        where j.captured_at is not null and j.result=jsonb_build_object('Bank',e.signed_bank,'Display',e.display_bank,'State',e.state,'Flags',e.flags,'FormulaVersion',e.formula_version,
            'AlphaUsed',e.alpha_used,'TodayDrain',e.drain_delta,'TodayRestore',e.restore_delta,'Components',e.components)`).Scan(&energyMatches)
	if err != nil || energyMatches != 1 {
		t.Fatalf("energy matches %d, error %v", energyMatches, err)
	}
	reader := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}, profileGraphSchema: graph}
	for metric, want := range map[string]int{"readiness-morning": 2, "energy": 1} {
		summary, err := reader.CompareLocalDecisionJournal(ctx, metric, input.Date)
		if err != nil || summary.Matched != want || summary.Recorded != want {
			t.Fatalf("%s journal parity: %+v, %v", metric, summary, err)
		}
	}
	// A journal insert error must not abort the surrounding business transaction.
	if _, err = tx.Exec(ctx, "update settings set value='true' where key=$1", SettingDecisionCapture); err != nil {
		t.Fatal(err)
	}
	brief := &health.BriefingResponse{ReadinessToday: 80}
	for _, headline := range []string{"", "stress", "stress"} {
		brief.Headline = &health.HeadlineSignal{Key: headline}
		decision, err := unit.liveDailyDecision(ctx, input.Date, brief)
		if err != nil || decision.Mode != "moderate" {
			t.Fatalf("unexpected Today decision %v, %v", decision, err)
		}
	}
	summary, err := reader.CompareLocalDecisionJournal(ctx, "today", input.Date)
	if err != nil || summary.Matched != 2 || summary.Recorded != 2 {
		t.Fatalf("Today journal parity: %+v, %v", summary, err)
	}
	if _, err = tx.Exec(ctx, "alter table hdbt_decision_inputs add constraint synthetic_failure check(false) not valid"); err != nil {
		t.Fatal(err)
	}
	run()
	count(5)
	// Rolling back a migration leaves the old solver available on this tenant.
	if _, err = tx.Exec(ctx, "alter table hdbt_decision_inputs rename to synthetic_hidden_inputs"); err != nil {
		t.Fatal(err)
	}
	run()
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// A true read-only consumer never attempts a receipt write.
	if _, err = db.pool.Exec(ctx, "insert into settings(key,value) values($1,'true')", SettingDecisionCapture); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.pool.Exec(context.Background(), "delete from settings where key=$1", SettingDecisionCapture); err != nil {
			t.Error(err)
		}
	})
	ro, err := db.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Rollback(ctx)
	unit = &DB{pool: &cacheTransaction{Tx: ro, ctx: ctx}}
	run()

}
