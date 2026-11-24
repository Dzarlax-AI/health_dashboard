package storage

import (
	"reflect"
	"testing"

	"health-receiver/internal/health"
)

func TestDashboardQualityPrimarySQL(t *testing.T) {
	db, done := testDB(t)
	defer done()
	component := func(value float64) health.ReadinessComponentEvidence {
		return health.ReadinessComponentEvidence{Value: &value, Present: true, Freshness: "ok", Confidence: "final"}
	}
	evidence := health.ReadinessEvidenceInput{SleepQuality: component(80), SleepDuration: component(7), SleepDeep: component(1), SleepREM: component(1.5), SleepAwake: component(0.2)}
	stale := evidence
	stale.SleepDeep.Freshness = "stale"
	partial := stale
	partial.SleepREM.Value = nil
	for _, e := range []*health.ReadinessEvidenceInput{nil, &evidence, &stale, &partial} {
		d := health.RawMetrics{ReadinessEvidence: e}
		_, _, got, _, err := db.briefingMetrics(t.Context(), &d)
		if err != nil {
			t.Fatal(err)
		}
		want := health.ComputeReadinessBriefing(d, "en").SleepQuality
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("quality got %+v want %+v", got, want)
		}
	}
}
