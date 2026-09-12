package health

import (
	"fmt"
	"testing"
	"time"
)

func TestCalculateSleepDurationBalanceUsesNoonBoundariesAndKeepsOutliers(t *testing.T) {
	loc := mustBalanceLocation(t, "Europe/Belgrade")
	date := "2026-09-11"
	result, err := CalculateSleepDurationBalance(date,
		[]SleepGoal{{EffectiveDate: "2026-08-01", Hours: 8, Version: "manual-v1"}},
		completeBalanceCoverage(t, date, loc),
		[]CompletedSleepEpisode{
			balanceEpisode("overnight", "2026-09-10 23:00", "2026-09-11 07:00", loc, NightDurationPlausible),
			balanceEpisode("prior-nap", "2026-09-10 15:00", "2026-09-10 16:00", loc, NightDurationPlausible),
			balanceEpisode("next-nap", "2026-09-11 14:00", "2026-09-11 15:00", loc, NightDurationPlausible),
			balanceEpisode("outlier", "2026-09-09 20:00", "2026-09-09 22:40", loc, NightDurationOutlier),
		}, loc)
	if err != nil {
		t.Fatalf("CalculateSleepDurationBalance: %v", err)
	}
	if result.State != SleepBalanceStateComplete || result.BalanceHours == nil || result.Confidence != SleepBalanceConfidenceLow {
		t.Fatalf("result = %#v", result)
	}
	last := result.Periods[len(result.Periods)-1]
	if last.TotalSleepHours != 9 || last.Start.Format("2006-01-02 15:04") != "2026-09-10 12:00" || last.End.Format("2006-01-02 15:04") != "2026-09-11 12:00" {
		t.Fatalf("last balance period = %#v", last)
	}
	if result.CalculatedThrough.Format(time.RFC3339) != "2026-09-11T12:00:00+02:00" {
		t.Fatalf("calculated through = %s", result.CalculatedThrough)
	}
}

func TestCalculateSleepDurationBalanceDoesNotCarryForwardIncompleteCoverage(t *testing.T) {
	loc := mustBalanceLocation(t, "Europe/Belgrade")
	date := "2026-09-11"
	coverage := completeBalanceCoverage(t, date, loc)
	coverage[4].CaptureState = SleepBalanceCoveragePartial
	result, err := CalculateSleepDurationBalance(date,
		[]SleepGoal{{EffectiveDate: "2026-08-01", Hours: 8, Version: "manual-v1"}}, coverage, nil, loc)
	if err != nil {
		t.Fatalf("CalculateSleepDurationBalance: %v", err)
	}
	if result.State != SleepBalanceStateIncomplete || result.BalanceHours != nil || result.IncompleteReason != "coverage_partial" {
		t.Fatalf("result = %#v", result)
	}
}

func TestCalculateSleepDurationBalanceRequiresManualGoalAndRejectsOverlap(t *testing.T) {
	loc := mustBalanceLocation(t, "Europe/Belgrade")
	date := "2026-09-11"
	coverage := completeBalanceCoverage(t, date, loc)
	withoutGoal, err := CalculateSleepDurationBalance(date, nil, coverage, nil, loc)
	if err != nil {
		t.Fatalf("CalculateSleepDurationBalance without goal: %v", err)
	}
	if withoutGoal.State != SleepBalanceStateIncomplete || withoutGoal.IncompleteReason != "goal_missing" || withoutGoal.BalanceHours != nil {
		t.Fatalf("without goal = %#v", withoutGoal)
	}
	_, err = CalculateSleepDurationBalance(date,
		[]SleepGoal{{EffectiveDate: "2026-08-01", Hours: 8, Version: "manual-v1"}}, coverage,
		[]CompletedSleepEpisode{
			balanceEpisode("one", "2026-09-10 22:00", "2026-09-11 06:00", loc, NightDurationPlausible),
			balanceEpisode("two", "2026-09-11 05:00", "2026-09-11 07:00", loc, NightDurationPlausible),
		}, loc)
	if err == nil {
		t.Fatal("overlapping episodes were accepted")
	}
	_, err = CalculateSleepDurationBalance(date,
		[]SleepGoal{
			{EffectiveDate: "2026-08-01", Hours: 8, Version: "manual-v1"},
			{EffectiveDate: "2026-08-01", Hours: 7.5, Version: "manual-v2"},
		}, coverage, nil, loc)
	if err == nil {
		t.Fatal("duplicate goal effective date was accepted")
	}
}

func completeBalanceCoverage(t *testing.T, wakeDate string, loc *time.Location) []SleepBalanceCoverage {
	t.Helper()
	end, err := time.ParseInLocation("2006-01-02", wakeDate, loc)
	if err != nil {
		t.Fatal(err)
	}
	coverage := make([]SleepBalanceCoverage, 0, SleepDurationBalanceWindowDays)
	for offset := SleepDurationBalanceWindowDays - 1; offset >= 0; offset-- {
		coverage = append(coverage, SleepBalanceCoverage{
			WakeDate: end.AddDate(0, 0, -offset).Format("2006-01-02"), CaptureState: SleepBalanceCoverageComplete, CoverageGeneration: "fixture-v1",
		})
	}
	return coverage
}

func balanceEpisode(id, start, end string, loc *time.Location, assessment string) CompletedSleepEpisode {
	const layout = "2006-01-02 15:04"
	startAt, _ := time.ParseInLocation(layout, start, loc)
	endAt, _ := time.ParseInLocation(layout, end, loc)
	return CompletedSleepEpisode{ID: id, Start: startAt, End: endAt, Source: "fixture-watch", InputHash: fmt.Sprintf("hash-%s", id), CaptureState: SleepBalanceCoverageComplete, DurationAssessment: assessment}
}

func mustBalanceLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}
