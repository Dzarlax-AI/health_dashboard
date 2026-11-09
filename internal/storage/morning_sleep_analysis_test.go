package storage

import (
	"health-receiver/internal/health"
	"testing"
	"time"
)

func TestMorningRecordedSleepAnalysisSeparatesSummaryAndIntervals(t *testing.T) {
	wake := time.Date(2026, 11, 9, 7, 0, 0, 0, time.UTC)
	segments := []wakeInputSegment{{Metric: "sleep_core", Source: "watch", Start: time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), Hours: 7}, {Metric: "sleep_core", Source: "watch", Start: wake.Add(-7*time.Hour - time.Minute), Hours: 3}, {Metric: "sleep_rem", Source: "watch", Start: wake.Add(-4 * time.Hour), Hours: 4}, {Metric: "sleep_unspecified", Source: "ring", Start: wake.Add(-8 * time.Hour), Hours: 8}}
	night := health.CompletedNightSleep{WakeDate: "2026-11-09", Source: "watch", DurationHours: 7, CaptureCompleteness: health.NightCaptureComplete, DurationAssessment: health.NightDurationPlausible}
	e, reason := buildRecordedSleepEpisode(segments, night, time.UTC)
	if e == nil || reason != "available" || e.AsleepMinutes != 420 || e.DeepMinutes != nil || e.CoreMinutes == nil || *e.CoreMinutes != 180 || e.UnclassifiedMinutes == nil || *e.UnclassifiedMinutes != 1 || e.AwakeMinutes != nil {
		t.Fatalf("night facts blocked by irrelevant summary, source or unclassified gap: %+v %s", e, reason)
	}
}
