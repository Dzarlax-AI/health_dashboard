package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"health-receiver/internal/health"
)

// Use the product calendar alignment and the independent Go score oracle.
func TestArchiveReadinessHistoryOracle(t *testing.T) {
	input := os.Getenv("HEALTH_HISTORY_ORACLE_INPUT")
	if input == "" {
		t.Skip("requires an isolated archive input")
	}
	data, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var days []struct {
		Day             string
		HRV, RHR, Sleep *float64
	}
	if err := json.Unmarshal(data, &days); err != nil {
		t.Fatal(err)
	}
	hrv, rhr, sleep := map[string]float64{}, map[string]float64{}, map[string]float64{}
	for _, day := range days {
		if day.HRV != nil {
			hrv[day.Day] = *day.HRV
		}
		if day.RHR != nil {
			rhr[day.Day] = *day.RHR
		}
		if day.Sleep != nil {
			sleep[day.Day] = *day.Sleep
		}
	}
	results := map[string][2]any{}
	for _, day := range days {
		score := health.ComputeReadinessScore(calendarAlignedValues(hrv, day.Day, 30), calendarAlignedValues(rhr, day.Day, 30), calendarAlignedValues(sleep, day.Day, 30))
		results[day.Day] = [2]any{score, health.ReadinessBand(score)}
	}
	payload, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(filepath.Dir(input), "scores.json"), payload, 0600); err != nil {
		t.Fatal(err)
	}
}
