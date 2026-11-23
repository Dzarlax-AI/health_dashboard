package dbtmetrics

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"health-receiver/internal/health"
)

// TestSemanticNightQualityParity compares the actual JSON returned by the card
// to the production Go formula with identical dated inputs, in one snapshot.
func TestSemanticNightQualityParity(t *testing.T) {
	ctx := t.Context()
	tx := semanticSleepReadTransaction(t)
	check := func(label, sql string) {
		t.Helper()
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var dates, scored, differences int
		for rows.Next() {
			var total, deep, rem, awake *float64
			var evidence health.ReadinessComponentEvidence
			var payload []byte
			if err := rows.Scan(&total, &deep, &rem, &awake, &evidence.Present, &evidence.Freshness, &evidence.Confidence, &payload); err != nil {
				t.Fatal(err)
			}
			expected := health.ComputeSleepQualityBreakdown(total, deep, rem, awake, evidence)
			want, err := json.Marshal(expected)
			if err != nil {
				t.Fatal(err)
			}
			var wantFields, gotFields map[string]any
			if err := json.Unmarshal(want, &wantFields); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(payload, &gotFields); err != nil {
				t.Fatal(err)
			}
			dates++
			if expected.ScorePct != nil {
				scored++
			}
			if !reflect.DeepEqual(wantFields, gotFields) {
				differences++
			}
		}
		if rows.Err() != nil {
			t.Fatal(rows.Err())
		}
		if dates == 0 || differences > 0 {
			t.Errorf("%s rows=%d mismatches=%d", label, dates, differences)
		}
		t.Logf("%s rows=%d scored=%d mismatches=%d", label, dates, scored, differences)
	}
	check("night_card", `select q.total_hours,q.deep_hours,q.rem_hours,q.awake_hours,q.evidence_present,q.evidence_freshness,q.evidence_confidence,c.card->'quality'
                       from dbt_output.semantic_sleep_quality_v1 q join dbt_output.semantic_sleep_night_card_v1 c using (wake_date)`)

	// Render the same one-parameter dbt macro against a typed fixture covering
	// every evidence branch, including stale inputs unavailable in a live night.
	raw, err := os.ReadFile("macros/semantic_sleep_quality.sql")
	if err != nil {
		t.Fatal(err)
	}
	body, ok := strings.CutPrefix(string(raw), "{% macro semantic_sleep_quality(inputs) %}")
	if !ok {
		t.Fatal("unexpected quality macro signature")
	}
	body, ok = strings.CutSuffix(strings.TrimSpace(body), "{% endmacro %}")
	if !ok {
		t.Fatal("unexpected end of quality macro")
	}
	const fixtures = `with checks(total_hours,deep_hours,rem_hours,awake_hours,evidence_present,evidence_freshness,evidence_confidence) as (values
       (null::float8,null::float8,null::float8,null::float8,false,'missing','provisional'),
       (0,null,null,null,false,'missing','provisional'),(6.28,null,null,null,true,'ok','final'),
       (6.28,1.21,1.56,null,true,'ok','final'),(7.25,1.15,1.6,0,true,'ok','final'),
       (10,0,0,1,true,'ok','final'),(7.1,0.7,1.0,0.8,true,'ok','low'),
       (7.1,0.7,1.0,0.8,true,'stale','provisional'),(7.1,0.7,1.0,0.8,true,'ok','provisional'),
       (7.1,0.7,1.0,0.8,false,'ok','final'),(0.04,0,0,0,true,'ok','final'))
       select total_hours,deep_hours,rem_hours,awake_hours,evidence_present,evidence_freshness,evidence_confidence,quality from (`
	check("formula_cases", fixtures+strings.ReplaceAll(body, "{{ inputs }}", "checks")+") q")
}
