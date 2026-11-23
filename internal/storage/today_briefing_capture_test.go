package storage

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

func TestInstalledTodayBriefingCapture(t *testing.T) {
	graph := os.Getenv("PROFILE_GRAPH_SCHEMA")
	if graph == "" || os.Getenv("HEALTH_PROFILE_GRAPH_PROBE") != "1" {
		t.Skip("requires the isolated full profile graph")
	}
	ctx := t.Context()
	db, err := NewWithSchema(ctx, os.Getenv("DATABASE_URL"), os.Getenv("PROFILE_SOURCE_SCHEMA"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	if _, err = tx.Exec(ctx, "insert into settings(key,value) values($1,'true') on conflict(key) do update set value=excluded.value", SettingDecisionCapture); err != nil {
		t.Fatal(err)
	}
	n := func(v float64) *float64 { return &v }
	evidence := func(metric string, v float64) health.ReadinessComponentEvidence {
		return health.ReadinessComponentEvidence{
			Metric: metric, Value: n(v), Present: true, Freshness: "ok", Confidence: "final", EvaluatedDate: "2026-01-05", SourceDate: "2026-01-05", SampleCount: 7}
	}
	d := health.RawMetrics{LastDate: "2026-01-05", Sleep: []float64{6}, RHR: []float64{59}, HRV: []float64{58},
		ReadinessEvidence: &health.ReadinessEvidenceInput{HRV: evidence("hrv", 58), RHR: evidence("rhr", 59),
			SleepQuality: evidence("quality", 75), SleepDuration: evidence("sleep", 6), SleepDeep: evidence("deep", 1), SleepREM: evidence("rem", 1.2), SleepAwake: evidence("awake", 0.3)}}
	projection, err := unit.morningReadinessProjection(ctx, &d)
	if err != nil {
		t.Fatal(err)
	}
	if projection.DisplayScore <= 65 {
		t.Fatal("expected a pre-headline score above the cap")
	}
	b := health.ComputeBriefingWithMetrics(d, "en", &projection, nil, nil)
	if b.Headline == nil || b.Headline.Key != "sleep_debt" || b.ReadinessDisplayScore != 65 {
		t.Fatal("expected a capped product briefing")
	}
	b.EnergyBank = &health.EnergyBank{Flags: []string{"imputed_activity"}}
	b.Sleep = &health.SleepAnalysis{LatestTotal: n(6)}
	b.IllnessSuspicion = &health.IllnessSuspicion{Signals: []health.IllnessEvidenceSignal{{Metric: "hrv", Kind: "physiological", Role: "support", Category: "autonomic", Direction: "lower", Strength: "weak", Status: "available", Contributes: true, Value: n(58), Baseline: n(60), DeltaAbs: n(-2), ZScore: n(-.3), Method: "test"}}}
	g := func(name string) string { return pgx.Identifier{graph, name}.Sanitize() }
	for _, illness := range []string{"none", "high"} {
		b.IllnessSuspicion.Confidence = illness
		health.ApplyIllnessSafetyCap(b, health.GetStrings("en"))
		if _, err = unit.liveDailyDecision(ctx, d.LastDate, b); err != nil {
			t.Fatal(err)
		}
		var key string
		if err = tx.QueryRow(ctx, "select 'briefing:'||input_hash from hdbt_decision_inputs where metric='today_briefing' and inputs->>'final_illness'=$1", illness).Scan(&key); err != nil {
			t.Fatal(err)
		}
		var exact bool
		if err = tx.QueryRow(ctx, `select raw_score=$2 and display_score=$3 and confidence=$4 and cap_reason=$5 and band=$6 and recovery_source=$7 and has_final_context is not false from (select *,display_score is not null as has_final_context from `+g("semantic_readiness_score_v1")+`) m where profile=current_user and case_id=$1`, key, b.ReadinessRawScore, b.ReadinessDisplayScore, b.ReadinessConfidence, b.ReadinessCapReason, b.ReadinessBand, b.RecoverySource).Scan(&exact); err != nil || !exact {
			t.Fatalf("final Readiness parity: %v (%t)", err, exact)
		}
		// The captured sleep quality must exactly reproduce all product factors.
		expected, err := json.Marshal(b.SleepQuality)
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.QueryRow(ctx, "select quality=$2::jsonb from "+g("captured_dashboard_sleep_quality_v1")+" where profile=current_user and capture=to_jsonb($1::text)", key, json.RawMessage(expected)).Scan(&exact); err != nil || !exact {
			t.Fatalf("sleep quality parity: %v (%t)", err, exact)
		}
		if err = tx.QueryRow(ctx, "select latest_total=6 and flags=array['imputed_activity'] and has_sleep and has_bank from "+g("captured_briefing_fields_v1")+" where profile=current_user and capture=to_jsonb($1::text)", key).Scan(&exact); err != nil || !exact {
			t.Fatalf("briefing fields parity: %v (%t)", err, exact)
		}
		if err = tx.QueryRow(ctx, `select count(*)=10 and count(*) filter(where component='HRV' and present and value=58 and evaluated_date='2026-01-05' and source_date='2026-01-05' and freshness='ok' and sample_count=7 and confidence='final' and missing_reason='')=1 and count(*) filter(where component='OvernightHR' and not present and value is null)=1 from `+g("captured_readiness_component_v1")+` where profile=current_user and capture=to_jsonb($1::text)`, key).Scan(&exact); err != nil || !exact {
			t.Fatalf("component parity: %v (%t)", err, exact)
		}
		if err = tx.QueryRow(ctx, `select metric='hrv' and kind='physiological' and role='support' and category='autonomic' and direction='lower' and strength='weak' and status='available' and contributes and value=58 and baseline=60 and delta_abs=-2 and z_score=-0.3 and method='test' from `+g("captured_illness_evidence_v1")+` where profile=current_user and capture=to_jsonb($1::text)`, key).Scan(&exact); err != nil || !exact {
			t.Fatalf("illness evidence parity: %v (%t)", err, exact)
		}
	}
	var withheld bool
	if err = tx.QueryRow(ctx, `select count(*)=1 and bool_and(raw_score is not null and solver_display_score is not null and display_score is null and confidence is null and cap_reason is null and band is null and recovery_source is null) from `+g("semantic_readiness_score_v1")+` where profile=current_user and case_id like 'journal:%' and reader='morning'`).Scan(&withheld); err != nil || !withheld {
		t.Fatalf("intermediate alias leak: %v (%t)", err, withheld)
	}
}
