package dbtmetrics_test

import (
	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
	"testing"
)

func TestProjectFamilyValues(t *testing.T) {
	if _, err := dbtmetrics.ProjectFamilyValues("sleep_comparison", health.SleepGoal{}); err == nil {
		t.Fatal("accepted wrong DTO")
	}
	values, err := dbtmetrics.ProjectFamilyValues("sleep_comparison", health.SleepDurationComparison{Status: "insufficient_history"})
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := values["sleep_comparison.delta_minutes"]; !ok || v != nil {
		t.Fatalf("missing nullable value: %v", values)
	}
	if values["sleep_comparison.status"] != "insufficient_history" {
		t.Fatal(values)
	}
}
