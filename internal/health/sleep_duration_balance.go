package health

import (
	"fmt"
	"sort"
	"time"
)

// SleepDurationBalanceWindowDays is deliberately a transparent accounting
// window. It is not a physiological debt or a recommendation for the next
// night.
const SleepDurationBalanceWindowDays = 14

const (
	SleepBalanceCoverageComplete = "complete"
	SleepBalanceCoveragePartial  = "partial"
	SleepBalanceCoverageUnknown  = "unknown"

	SleepBalanceStateComplete   = "complete"
	SleepBalanceStateIncomplete = "incomplete"

	SleepBalanceConfidenceNormal = "normal"
	SleepBalanceConfidenceLow    = "low"
)

// SleepGoal is a manual, versioned user target. It intentionally does not
// claim to be a biological sleep need. A new effective date supersedes the
// prior target only for later balance periods.
type SleepGoal struct {
	EffectiveDate string
	Hours         float64
	Version       string
}

// ValidateSleepGoal keeps the request boundary and durable storage aligned on
// the small, user-facing manual-goal contract. It does not assign medical
// meaning to the target; it only validates a dated preference.
func ValidateSleepGoal(goal SleepGoal) error {
	if goal.Version == "" || goal.Hours < 3 || goal.Hours > 14 {
		return fmt.Errorf("invalid manual sleep goal")
	}
	if _, err := time.Parse("2006-01-02", goal.EffectiveDate); err != nil {
		return fmt.Errorf("invalid sleep goal effective date %q", goal.EffectiveDate)
	}
	return nil
}

// CompletedSleepEpisode is a source-selected, deduplicated episode emitted by
// the controlled adapter. The balance calculator rejects overlapping episodes
// rather than silently double counting them.
type CompletedSleepEpisode struct {
	ID                 string
	Start              time.Time
	End                time.Time
	Source             string
	SourceEpoch        string
	InputHash          string
	CoverageGeneration string
	CaptureState       string
	DurationAssessment string
}

// SleepBalanceCoverage closes one tenant-local [D-1 noon, D noon) period.
// Complete coverage can validly contain no episodes; missing or partial
// coverage must remain incomplete and must never be filled from an average.
type SleepBalanceCoverage struct {
	WakeDate           string
	CaptureState       string
	CoverageGeneration string
}

type SleepDurationBalancePeriod struct {
	WakeDate           string
	Start              time.Time
	End                time.Time
	GoalHours          float64
	TotalSleepHours    float64
	DeltaHours         float64
	CaptureState       string
	DurationAssessment string
}

// SleepDurationBalance is an explainable arithmetic result. BalanceHours is
// nil unless all fourteen required periods have an explicit complete coverage
// commitment and an effective manual goal.
type SleepDurationBalance struct {
	WakeDate          string
	WindowStartDate   string
	CalculatedThrough time.Time
	State             string
	Confidence        string
	IncompleteReason  string
	LastCompleteDate  string
	BalanceHours      *float64
	Periods           []SleepDurationBalancePeriod
}

// CalculateSleepDurationBalance sums source-selected episodes against the
// goal in each of the fourteen tenant-local noon-to-noon periods ending on
// wakeDate. It is intentionally independent of readiness, EnergyBank, sleep
// stages, and any future model state.
func CalculateSleepDurationBalance(wakeDate string, goals []SleepGoal, coverages []SleepBalanceCoverage, episodes []CompletedSleepEpisode, loc *time.Location) (SleepDurationBalance, error) {
	if loc == nil {
		return SleepDurationBalance{}, fmt.Errorf("sleep duration balance requires a tenant timezone")
	}
	endDay, err := time.ParseInLocation("2006-01-02", wakeDate, loc)
	if err != nil {
		return SleepDurationBalance{}, fmt.Errorf("invalid balance wake date: %w", err)
	}
	if err := validateSleepGoals(goals, loc); err != nil {
		return SleepDurationBalance{}, err
	}
	if err := validateSleepEpisodes(episodes); err != nil {
		return SleepDurationBalance{}, err
	}

	coverageByDate := make(map[string]SleepBalanceCoverage, len(coverages))
	for _, coverage := range coverages {
		if _, err := time.ParseInLocation("2006-01-02", coverage.WakeDate, loc); err != nil {
			return SleepDurationBalance{}, fmt.Errorf("invalid balance coverage date %q", coverage.WakeDate)
		}
		if _, exists := coverageByDate[coverage.WakeDate]; exists {
			return SleepDurationBalance{}, fmt.Errorf("duplicate balance coverage for %s", coverage.WakeDate)
		}
		coverageByDate[coverage.WakeDate] = coverage
	}

	windowStart := endDay.AddDate(0, 0, -(SleepDurationBalanceWindowDays - 1))
	result := SleepDurationBalance{
		WakeDate:          wakeDate,
		WindowStartDate:   windowStart.Format("2006-01-02"),
		CalculatedThrough: balancePeriodEnd(endDay),
		State:             SleepBalanceStateComplete,
		Confidence:        SleepBalanceConfidenceNormal,
		Periods:           make([]SleepDurationBalancePeriod, 0, SleepDurationBalanceWindowDays),
	}

	var balance float64
	for offset := 0; offset < SleepDurationBalanceWindowDays; offset++ {
		day := windowStart.AddDate(0, 0, offset)
		date := day.Format("2006-01-02")
		coverage, covered := coverageByDate[date]
		if !covered || coverage.CaptureState != SleepBalanceCoverageComplete {
			result.State = SleepBalanceStateIncomplete
			result.IncompleteReason = incompleteSleepBalanceReason(coverage, covered)
			return result, nil
		}
		goal, found := sleepGoalForDate(goals, day, loc)
		if !found {
			result.State = SleepBalanceStateIncomplete
			result.IncompleteReason = "goal_missing"
			return result, nil
		}

		start, end := balancePeriodBounds(day)
		total, assessment := balanceEpisodeHours(episodes, start, end)
		period := SleepDurationBalancePeriod{
			WakeDate: date, Start: start, End: end, GoalHours: goal.Hours,
			TotalSleepHours: total, DeltaHours: total - goal.Hours,
			CaptureState: coverage.CaptureState, DurationAssessment: assessment,
		}
		result.Periods = append(result.Periods, period)
		balance += period.DeltaHours
		result.LastCompleteDate = date
		if assessment == NightDurationOutlier {
			result.Confidence = SleepBalanceConfidenceLow
		}
	}
	result.BalanceHours = &balance
	return result, nil
}

func validateSleepGoals(goals []SleepGoal, loc *time.Location) error {
	effectiveDates := make(map[string]struct{}, len(goals))
	for _, goal := range goals {
		if goal.Version == "" || goal.Hours < 3 || goal.Hours > 14 {
			return fmt.Errorf("invalid manual sleep goal")
		}
		if _, err := time.ParseInLocation("2006-01-02", goal.EffectiveDate, loc); err != nil {
			return fmt.Errorf("invalid sleep goal effective date %q", goal.EffectiveDate)
		}
		if _, exists := effectiveDates[goal.EffectiveDate]; exists {
			return fmt.Errorf("duplicate sleep goal effective date %q", goal.EffectiveDate)
		}
		effectiveDates[goal.EffectiveDate] = struct{}{}
	}
	return nil
}

func validateSleepEpisodes(episodes []CompletedSleepEpisode) error {
	ordered := append([]CompletedSleepEpisode(nil), episodes...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start.Before(ordered[j].Start) })
	for index, episode := range ordered {
		if episode.ID == "" || episode.Start.IsZero() || !episode.End.After(episode.Start) || episode.Source == "" || episode.InputHash == "" {
			return fmt.Errorf("invalid completed sleep episode")
		}
		if index > 0 && episode.Start.Before(ordered[index-1].End) {
			return fmt.Errorf("overlapping completed sleep episodes %q and %q", ordered[index-1].ID, episode.ID)
		}
	}
	return nil
}

func sleepGoalForDate(goals []SleepGoal, date time.Time, loc *time.Location) (SleepGoal, bool) {
	var selected SleepGoal
	selectedDate := time.Time{}
	for _, goal := range goals {
		effective, _ := time.ParseInLocation("2006-01-02", goal.EffectiveDate, loc)
		if effective.After(date) || (!selectedDate.IsZero() && !effective.After(selectedDate)) {
			continue
		}
		selected, selectedDate = goal, effective
	}
	return selected, !selectedDate.IsZero()
}

func balancePeriodBounds(day time.Time) (time.Time, time.Time) {
	end := balancePeriodEnd(day)
	return end.AddDate(0, 0, -1), end
}

func balancePeriodEnd(day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), 12, 0, 0, 0, day.Location())
}

func balanceEpisodeHours(episodes []CompletedSleepEpisode, start, end time.Time) (float64, string) {
	var total float64
	assessment := NightDurationPlausible
	for _, episode := range episodes {
		if !episode.End.After(start) || !episode.Start.Before(end) {
			continue
		}
		clippedStart, clippedEnd := episode.Start, episode.End
		if clippedStart.Before(start) {
			clippedStart = start
		}
		if clippedEnd.After(end) {
			clippedEnd = end
		}
		total += clippedEnd.Sub(clippedStart).Hours()
		if episode.DurationAssessment == NightDurationOutlier {
			assessment = NightDurationOutlier
		}
	}
	// An outlier is an observation-quality annotation, not a reason to erase
	// arithmetic. Evaluate the whole noon-to-noon period as well as any
	// adapter-marked episode: stage fragments naturally split one short night
	// into many individually plausible intervals.
	if total > 0 && (total < 3 || total > 14) {
		assessment = NightDurationOutlier
	}
	return total, assessment
}

func incompleteSleepBalanceReason(coverage SleepBalanceCoverage, covered bool) string {
	if !covered {
		return "coverage_missing"
	}
	switch coverage.CaptureState {
	case SleepBalanceCoveragePartial:
		return "coverage_partial"
	case SleepBalanceCoverageUnknown:
		return "coverage_unknown"
	default:
		return "coverage_invalid"
	}
}
