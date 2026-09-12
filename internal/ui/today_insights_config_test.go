package ui

import (
	"errors"
	"testing"

	"health-receiver/internal/storage"
)

func TestTodayInsightsConfigRejectsB1EnableWithoutQualityGate(t *testing.T) {
	_, err := todayInsightsConfigSettings(map[string]bool{storage.SettingTodayInsightsB1Enabled: true}, false)
	if !errors.Is(err, errTodayInsightsB1QualityGateRequired) {
		t.Fatalf("error = %v, want quality-gate requirement", err)
	}
}

func TestTodayInsightsConfigAllowsB1EnableAfterQualityGate(t *testing.T) {
	settings, err := todayInsightsConfigSettings(map[string]bool{storage.SettingTodayInsightsB1Enabled: true}, true)
	if err != nil {
		t.Fatalf("todayInsightsConfigSettings: %v", err)
	}
	if got := settings[storage.SettingTodayInsightsB1Enabled]; got != "true" {
		t.Fatalf("B1 setting = %q", got)
	}
}
