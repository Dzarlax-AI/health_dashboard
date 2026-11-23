package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

// TestLocalTodayArchiveParity is opt-in and requires an explicitly marked
// disposable profile copy. It prints counts only, including SQL NULLs.
func TestLocalTodayArchiveParity(t *testing.T) {
	if os.Getenv("HEALTH_TODAY_ARCHIVE_PROBE") != "1" {
		t.Skip("local archive only")
	}
	ctx := t.Context()
	source, graph := os.Getenv("PROFILE_SOURCE_SCHEMA"), os.Getenv("PROFILE_GRAPH_SCHEMA")
	isolated, err := NewLocalProfileGraphReader(ctx, os.Getenv("DATABASE_URL"), source, graph)
	if err != nil {
		t.Fatal(err)
	}
	defer isolated.Close()
	db, err := NewWithSchema(ctx, os.Getenv("DATABASE_URL"), source)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var marked bool
	if err = db.pool.QueryRow(ctx, "select exists(select from local_today_archive_marker where disposable)").Scan(&marked); err != nil || !marked {
		t.Fatal("missing local rehearsal marker")
	}
	if _, err = db.pool.Exec(ctx, "insert into settings(key,value) values($1,'true') on conflict(key) do update set value=excluded.value", SettingDecisionCapture); err != nil {
		t.Fatal(err)
	}
	b, err := db.GetHealthBriefing("en")
	if err != nil {
		t.Fatalf("briefing: %v", err)
	}
	if b.RawMetrics == nil || b.ReadinessServing == nil || b.DailyDecision == nil {
		t.Fatal("incomplete product briefing")
	}
	h, err := isolated.CaptureLocalReadinessDecision(ctx, b.Date, "history")
	if err != nil {
		t.Fatal(err)
	}
	if err = isolated.VerifyLocalReadinessDecision(ctx, h); err != nil {
		t.Fatal(err)
	}
	fmt.Println(`MATRIX ["readiness_history.band",1,1,0]`)
	var briefing, decision string
	if err = db.pool.QueryRow(ctx, "select 'briefing:'||input_hash from hdbt_decision_inputs where metric='today_briefing' and decision_key=$1", b.Date).Scan(&briefing); err != nil {
		t.Fatal(err)
	}
	if err = db.pool.QueryRow(ctx, "select 'journal:'||input_hash from hdbt_decision_inputs where metric='today_decision' and decision_key=$1", b.Date).Scan(&decision); err != nil {
		t.Fatal(err)
	}
	// Expected rows come from the original Go response. Native sample keys
	// preserve component identity and signal order. Comparison occurs in SQL.
	type rec map[string]any
	queryRows := func(model, filter string, id any, order string) []rec {
		t.Helper()
		rows, err := db.pool.Query(ctx, "select to_jsonb(m) from "+pgx.Identifier{graph, model}.Sanitize()+" m where m.profile=current_user and "+filter+" order by "+order, id)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := pgx.CollectRows(rows, pgx.RowTo[rec])
		if err != nil {
			t.Fatal(err)
		}
		return actual
	}
	object := func(v any) rec {
		t.Helper()
		s, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var out rec
		if err = json.Unmarshal(s, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	compare := func(prefix string, fields map[string]string, expected, actual []rec) {
		t.Helper()
		for id, col := range fields {
			var dif int
			limit := min(len(expected), len(actual))
			for i := 0; i < limit; i++ {
				left, _ := json.Marshal(expected[i][col])
				right, _ := json.Marshal(actual[i][col])
				var equal bool
				if err := db.pool.QueryRow(ctx, "select $1::jsonb=$2::jsonb", json.RawMessage(left), json.RawMessage(right)).Scan(&equal); err != nil {
					t.Fatal(err)
				}
				if !equal {
					dif++
				}
			}
			dif += max(len(expected), len(actual)) - limit
			r, _ := json.Marshal([]any{prefix + id, len(expected), len(actual), dif})
			fmt.Println("MATRIX", string(r))
			if dif > 0 {
				t.Errorf("%s%s mismatches=%d", prefix, id, dif)
			}
		}
	}
	same := func(keys ...string) map[string]string {
		m := map[string]string{}
		for _, k := range keys {
			m[k] = k
		}
		return m
	}
	native := "m.capture=to_jsonb($1::text)"
	ready := rec{"raw_score": b.ReadinessRawScore, "display_score": b.ReadinessDisplayScore, "band": b.ReadinessBand,
		"confidence": b.ReadinessConfidence, "cap_reason": nil, "recovery_source": b.RecoverySource}
	if b.ReadinessCapReason != "" {
		ready["cap_reason"] = b.ReadinessCapReason
	}
	readyRows := queryRows("semantic_readiness_score_v1", "m.case_id=$1", briefing, "case_id")
	compare("readiness.", map[string]string{"readiness_raw_score": "raw_score", "readiness_display_score": "display_score", "readiness_confidence": "confidence", "readiness_cap_reason": "cap_reason", "readiness_band": "band", "recovery_source": "recovery_source"}, []rec{ready}, readyRows)
	compare("readiness.", map[string]string{"readiness_score": "display_score"}, []rec{{"display_score": b.ReadinessScore}}, readyRows)
	compare("readiness.", map[string]string{"readiness_today": "display_score", "readiness_today_band": "band"}, []rec{{"display_score": b.ReadinessToday, "band": b.ReadinessTodayBand}}, readyRows)
	compare("readiness.", map[string]string{"recovery_pct": "display_score"}, []rec{{"display_score": b.RecoveryPct}}, readyRows)
	qual := object(b.SleepQuality)
	compare("sleep_quality.", same("score_pct", "duration_pct", "continuity_pct", "structure_pct", "confidence"), []rec{qual}, queryRows("captured_dashboard_sleep_quality_v1", native, briefing, "capture"))
	var illnessRows []rec
	if b.IllnessSuspicion != nil {
		for _, sig := range b.IllnessSuspicion.Signals {
			illnessRows = append(illnessRows, object(sig))
		}
	}
	compare("illness_evidence.", same("role", "category", "direction", "strength", "status", "contributes", "value", "baseline", "delta_abs", "z_score", "method"), illnessRows, queryRows("captured_illness_evidence_v1", native, briefing, "ordinality"))
	var components []rec
	if evidence := b.RawMetrics.ReadinessEvidence; evidence != nil {
		typed := map[string]health.ReadinessComponentEvidence{"HRV": evidence.HRV, "RHR": evidence.RHR, "OvernightHR": evidence.OvernightHR, "SleepDuration": evidence.SleepDuration, "SleepQuality": evidence.SleepQuality, "SleepDeep": evidence.SleepDeep, "SleepREM": evidence.SleepREM, "SleepCore": evidence.SleepCore, "SleepAwake": evidence.SleepAwake, "Respiratory": evidence.Respiratory}
		names := make([]string, 0, len(typed))
		for k := range typed {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			c := typed[name]
			components = append(components, rec{"value": c.Value, "present": c.Present, "freshness": c.Freshness, "sample_count": c.SampleCount, "confidence": c.Confidence, "missing_reason": c.MissingReason})
		}
	}
	compare("readiness_component.", same("value", "present", "freshness", "sample_count", "confidence", "missing_reason"), components, queryRows("captured_readiness_component_v1", native, briefing, `component collate "C"`))
	var exp []rec
	if b.Sleep != nil {
		exp = []rec{{"latest_total": b.Sleep.LatestTotal}}
	}
	compare("sleep_summary.", same("latest_total"), exp, queryRows("captured_briefing_fields_v1", native+" and has_sleep", briefing, "capture"))
	exp = nil
	if b.EnergyBank != nil {
		exp = []rec{{"flags": b.EnergyBank.Flags}}
	}
	compare("energy_bank.", same("flags"), exp, queryRows("captured_briefing_fields_v1", native+" and has_bank", briefing, "capture"))
	compare("today_decision.", map[string]string{"mode": "decision_mode", "signal_keys": "signal_keys", "evidence_domains": "evidence_domains"}, []rec{{"decision_mode": b.DailyDecision.Mode, "signal_keys": b.DailyDecision.SignalKeys, "evidence_domains": b.DailyDecision.EvidenceDomains}}, queryRows("daily_decision_boundary_v1", native, decision, "capture"))
}
