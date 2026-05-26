package storage

import (
	"testing"
	"time"
)

func archSeg(date, metric, source string, hours float64) SleepArchitectureRawSegment {
	return SleepArchitectureRawSegment{
		Date:       date,
		MetricName: metric,
		Source:     source,
		Hours:      hours,
	}
}

func mustParseDateForTest(t *testing.T, date string) time.Time {
	t.Helper()
	out, err := time.Parse(isoDate, date)
	if err != nil {
		t.Fatalf("parse date %s: %v", date, err)
	}
	return out
}

func TestComputeSleepArchitectureDays_PicksOneSourceAndSeparatesWakeSignals(t *testing.T) {
	rows := []SleepArchitectureRawSegment{
		archSeg("2026-05-01 23:00:00 +0000", "sleep_core", "Apple Watch", 2.0),
		archSeg("2026-05-02 01:45:00 +0000", "sleep_rem", "Apple Watch", 2.0),
		archSeg("2026-05-02 04:00:00 +0000", "sleep_deep", "Apple Watch", 2.0),
		archSeg("2026-05-02 01:00:00 +0000", "sleep_awake", "Apple Watch", 0.25),
		archSeg("2026-05-01 23:00:00 +0000", "sleep_unspecified", "RingConn", 7.5),
		archSeg("2026-05-02 02:00:00 +0000", "sleep_awake", "RingConn", 1.0),
	}

	got := ComputeSleepArchitectureDays(rows)["2026-05-02"]
	if !got.Eligible || got.Reason != SleepArchitectureReasonOK {
		t.Fatalf("expected eligible ok day, got eligible=%v reason=%q", got.Eligible, got.Reason)
	}
	if got.Source != "Apple Watch" {
		t.Fatalf("source = %q, want Apple Watch", got.Source)
	}
	if got.ExplicitWakeBouts != 1 {
		t.Fatalf("explicit wake bouts = %d, want 1", got.ExplicitWakeBouts)
	}
	if got.GapInferredWakeBouts != 1 {
		t.Fatalf("gap inferred wake bouts = %d, want 1", got.GapInferredWakeBouts)
	}
	if got.WASOHours != 0.25 {
		t.Fatalf("WASOHours = %v, want 0.25", got.WASOHours)
	}
	if got.FragmentationIndex <= 0 {
		t.Fatalf("fragmentation index should be positive, got %v", got.FragmentationIndex)
	}
}

func TestComputeSleepArchitectureDays_CoarseOnlyIsUnavailableNotZero(t *testing.T) {
	rows := []SleepArchitectureRawSegment{
		archSeg("2026-05-01 23:00:00 +0000", "sleep_unspecified", "RingConn", 7.0),
	}

	got := ComputeSleepArchitectureDays(rows)["2026-05-02"]
	if got.Eligible {
		t.Fatal("coarse-only source must not be architecture-eligible")
	}
	if got.Reason != SleepArchitectureReasonCoarseOnly {
		t.Fatalf("reason = %q, want %q", got.Reason, SleepArchitectureReasonCoarseOnly)
	}
	if got.FragmentationIndex != 0 || got.WASOHours != 0 || got.ExplicitWakeBouts != 0 {
		t.Fatalf("coarse-only unavailable day must not fabricate architecture metrics: %+v", got)
	}
	if got.Confidence != SleepArchitectureConfidenceUnavailable {
		t.Fatalf("confidence = %q, want unavailable", got.Confidence)
	}
}

func TestBuildSleepArchitectureWindow_ReportsCoverageAndConfidence(t *testing.T) {
	days := ComputeSleepArchitectureDays([]SleepArchitectureRawSegment{
		archSeg("2026-05-01 23:00:00 +0000", "sleep_core", "Apple Watch", 7.0),
		archSeg("2026-05-02 23:00:00 +0000", "sleep_unspecified", "RingConn", 7.0),
	})
	end := mustParseDateForTest(t, "2026-05-03")

	got := BuildSleepArchitectureWindow(end, 3, days)
	if got.Days != 3 {
		t.Fatalf("Days = %d, want 3", got.Days)
	}
	if got.EligibleDays != 1 {
		t.Fatalf("EligibleDays = %d, want 1", got.EligibleDays)
	}
	if got.SourceDays != 2 {
		t.Fatalf("SourceDays = %d, want 2", got.SourceDays)
	}
	if got.Confidence != "partial" {
		t.Fatalf("Confidence = %q, want partial", got.Confidence)
	}
	if got.MissingReasonCounts[SleepArchitectureReasonCoarseOnly] != 1 {
		t.Fatalf("coarse-only count = %d, want 1", got.MissingReasonCounts[SleepArchitectureReasonCoarseOnly])
	}
	if got.MissingReasonCounts[SleepArchitectureReasonMissingSegments] != 1 {
		t.Fatalf("missing count = %d, want 1", got.MissingReasonCounts[SleepArchitectureReasonMissingSegments])
	}
	if got.WASOHours == nil || *got.WASOHours != 0 {
		t.Fatalf("WASO average = %v, want 0", got.WASOHours)
	}
}
