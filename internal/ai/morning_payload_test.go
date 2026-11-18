package ai

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"health-receiver/internal/health"
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

func TestMorningComparisonUsesOnlyObservedFacts(t *testing.T) {
	for _, tc := range []struct{ capture, key string }{
		{health.NightCaptureComplete, "duration_comparison"},
		{health.NightCapturePartial, "comparison"},
	} {
		t.Run(tc.capture, func(t *testing.T) {
			hours, duration, reference, delta := 7.0, 420, 440, -20
			c := health.NewSleepDurationComparison(time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC))
			c.Status, c.DurationMinutes, c.ReferenceMinutes, c.DeltaMinutes = health.SleepComparisonAvailable, &duration, &reference, &delta
			c.Finalization, c.Capture, c.Assessment = health.NightFinalProvisional, tc.capture, health.NightDurationPlausible
			e := health.MorningInsightEvidence{Date: c.WakeDate, NightSleep: &health.MorningReportSleep{
				Date: c.WakeDate, ReportDate: c.WakeDate, Hours: &hours, Capture: tc.capture,
				Assessment: health.NightDurationPlausible, Finalization: health.NightFinalProvisional, Comparison: &c,
			}}
			before, err := MorningProviderPayload(e)
			if err != nil {
				t.Fatal(err)
			}
			var packet struct {
				Night map[string]json.RawMessage `json:"night_sleep"`
			}
			if err := json.Unmarshal(before, &packet); err != nil {
				t.Fatal(err)
			}
			var comparison map[string]any
			if err := json.Unmarshal(packet.Night[tc.key], &comparison); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"capture", "assessment", "finalization", "observed_at", "fact_version", "rule_version"} {
				if _, ok := comparison[key]; ok {
					t.Errorf("comparison leaked %s", key)
				}
			}
			if comparison["status"] != c.Status || comparison["delta_minutes"] != float64(delta) || comparison["reference_minutes"] != float64(reference) {
				t.Fatalf("missing comparison facts: %v", comparison)
			}
			hash := HashInsightBundle(e)
			now := time.Now()
			c.Finalization, c.ObservedAt, c.FactVersion = "final", &now, "new-revision"
			after, err := MorningProviderPayload(e)
			if err != nil || !bytes.Equal(before, after) || HashInsightBundle(e) != hash {
				t.Fatal("comparison metadata changed the packet or cache key")
			}
			delta++
			if HashInsightBundle(e) == hash {
				t.Fatal("changed comparison fact did not invalidate the bundle")
			}
		})
	}
}
