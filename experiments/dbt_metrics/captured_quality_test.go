package dbtmetrics

import (
	"encoding/json"
	"os"
	"testing"

	"health-receiver/internal/health"
)

func TestCapturedDashboardSleepQuality(t *testing.T) {
	path := os.Getenv("BRIEFING_COMPONENTS_FIXTURE")
	if path == "" {
		t.Skip("requires disposable briefing fixture")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct{ Raw health.RawMetrics }
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	var expected []*health.SleepQualityBreakdown
	for _, c := range cases {
		expected = append(expected, health.ComputeReadinessBriefing(c.Raw, "en").SleepQuality)
	}
	data, err = json.Marshal(expected)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".quality", data, 0600); err != nil {
		t.Fatal(err)
	}
}
