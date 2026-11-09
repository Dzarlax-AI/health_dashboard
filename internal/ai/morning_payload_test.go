package ai

import (
	"encoding/json"
	"health-receiver/internal/health"
	"strings"
	"testing"
	"time"
)

func TestMorningAnalysisPacketContainsObservationsWithoutFinalizationBoilerplate(t *testing.T) {
	hours, mean, calories := 6.85, 7.59, 560.14905
	deep, rem, awake, diff := 49, 107, 7, -44
	start := time.Date(2026, 11, 9, 1, 51, 57, 0, time.FixedZone("CET", 3600))
	evidence := health.MorningInsightEvidence{Date: "2026-11-09", NightSleep: &health.MorningReportSleep{ReportDate: "2026-11-09", Date: "2026-11-09", Hours: &hours, Capture: health.NightCaptureComplete, Assessment: health.NightDurationPlausible, Finalization: health.NightFinalProvisional, InputHash: "internal-hash", AnalysisStatus: "available", RecentRecordedMeanHours: &mean, RecentRecordedNights: 7, RecentRecordedDifferenceMinutes: &diff, Episode: &health.MorningSleepEpisode{Start: &start, DeepMinutes: &deep, REMMinutes: &rem, AwakeMinutes: &awake}, YesterdayActivity: &health.MorningYesterdayActivity{Date: "2026-11-08", Calories: &calories}}}
	encoded, err := MorningProviderPayload(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, technical := range []string{"finalization", "provisional", "capture", "analysis_status", "internal-hash", "560.14905", "01:51:57", "7.59"} {
		if strings.Contains(string(encoded), technical) {
			t.Fatalf("technical or unreadable value exposed to model: %s", encoded)
		}
	}
	var payload map[string]any
	if json.Unmarshal(encoded, &payload) != nil {
		t.Fatal("invalid packet")
	}
	night := payload["night_sleep"].(map[string]any)
	if night["recorded_sleep_minutes"] != float64(411) || night["recent_recorded_mean_minutes"] != float64(455) || night["episode"].(map[string]any)["deep_minutes"] != float64(49) || night["yesterday_activity"].(map[string]any)["calories"] != float64(560) {
		t.Fatalf("night facts lost: %s", encoded)
	}
	hash := HashInsightBundle(evidence)
	evidence.NightSleep.InputHash = "new-source-generation"
	if HashInsightBundle(evidence) == hash {
		t.Fatal("source identity lost from cache validation")
	}
}
