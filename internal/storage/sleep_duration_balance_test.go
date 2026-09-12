package storage

import (
	"testing"
	"time"

	"health-receiver/internal/health"
)

func TestCompletedSleepEpisodesMustStayWithinOneCompletePeriodAndNotOverlap(t *testing.T) {
	start := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	coverage := SleepPeriodCoverageCommitment{
		WakeDate: "2026-09-10", SourceEpoch: "health-sync-ios-v1",
		CaptureCompleteness: health.SleepBalanceCoverageComplete, CoverageGeneration: "period-1",
		CoveredIntervalStart: start, CoveredIntervalEnd: start.Add(24 * time.Hour),
		ObservedAt: start.Add(25 * time.Hour), InputHash: "coverage-hash",
	}
	valid := []CompletedSleepEpisodeCommitment{
		testEpisode("one", coverage, start.Add(10*time.Hour), start.Add(14*time.Hour)),
		testEpisode("two", coverage, start.Add(15*time.Hour), start.Add(16*time.Hour)),
	}
	if err := validateCompletedSleepEpisodeCommitments(coverage, valid); err != nil {
		t.Fatalf("valid episodes rejected: %v", err)
	}
	overlapping := append([]CompletedSleepEpisodeCommitment(nil), valid...)
	overlapping[1].Start = start.Add(13 * time.Hour)
	if err := validateCompletedSleepEpisodeCommitments(coverage, overlapping); err == nil {
		t.Fatal("overlapping episodes accepted")
	}
	outside := valid[:1]
	outside[0].Start = start.Add(-time.Minute)
	if err := validateCompletedSleepEpisodeCommitments(coverage, outside); err == nil {
		t.Fatal("episode outside coverage accepted")
	}
}

func TestSleepDurationBalanceInputHashIsStableAcrossInputOrder(t *testing.T) {
	goals := []health.SleepGoal{
		{EffectiveDate: "2026-09-02", Hours: 7.5, Version: "manual-v1"},
		{EffectiveDate: "2026-09-01", Hours: 8, Version: "manual-v1"},
	}
	coverage := []health.SleepBalanceCoverage{
		{WakeDate: "2026-09-02", CaptureState: health.SleepBalanceCoverageComplete, CoverageGeneration: "b"},
		{WakeDate: "2026-09-01", CaptureState: health.SleepBalanceCoverageComplete, CoverageGeneration: "a"},
	}
	start := time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC)
	episodes := []health.CompletedSleepEpisode{
		{ID: "b", Start: start.Add(24 * time.Hour), End: start.Add(31 * time.Hour), Source: "Watch", InputHash: "b"},
		{ID: "a", Start: start, End: start.Add(7 * time.Hour), Source: "Watch", InputHash: "a"},
	}
	first, err := sleepDurationBalanceInputHash(goals, coverage, episodes)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sleepDurationBalanceInputHash(
		[]health.SleepGoal{goals[1], goals[0]},
		[]health.SleepBalanceCoverage{coverage[1], coverage[0]},
		[]health.CompletedSleepEpisode{episodes[1], episodes[0]},
	)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("input hash changed with order: %s != %s", first, second)
	}
}

func testEpisode(id string, coverage SleepPeriodCoverageCommitment, start, end time.Time) CompletedSleepEpisodeCommitment {
	return CompletedSleepEpisodeCommitment{
		EpisodeID: id, WakeDate: coverage.WakeDate, Start: start, End: end,
		Source: "Apple Watch", SourceEpoch: coverage.SourceEpoch, InputHash: "hash-" + id,
		CoverageGeneration: coverage.CoverageGeneration, CaptureState: health.SleepBalanceCoverageComplete,
		DurationAssessment: health.NightDurationPlausible, ObservedAt: coverage.ObservedAt,
	}
}
