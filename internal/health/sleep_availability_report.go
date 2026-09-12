package health

import (
	"fmt"
	"time"
)

// RecentSleepAvailabilityReportVersion identifies the fixed, aggregate-only
// pre-rollout report for the B0 sleep claim. It contains no dates, values,
// sources, or stable identifiers, so it can be attached to a release review
// without exporting a tenant's health history.
const RecentSleepAvailabilityReportVersion = "recent-sleep-availability-v1"

type RecentSleepAvailabilityReport struct {
	Version                  string         `json:"version"`
	EvaluationDays           int            `json:"evaluation_days"`
	ClaimStates              map[string]int `json:"claim_states"`
	UnknownReasons           map[string]int `json:"unknown_reasons"`
	ActionEvents             int            `json:"action_events"`
	TrueWithoutActionReasons map[string]int `json:"true_without_action_reasons"`
}

// BuildRecentSleepAvailabilityReport replays the closed B0 policy across a
// bounded local-date window. Historical dates are evaluated one minute after
// the evening finalization boundary; the report's through date uses asOf so
// the result honestly shows a still-provisional current evening.
func BuildRecentSleepAvailabilityReport(records []CompletedNightSleep, throughDate string, days int, asOf time.Time, loc *time.Location) (RecentSleepAvailabilityReport, error) {
	if loc == nil {
		return RecentSleepAvailabilityReport{}, fmt.Errorf("availability report requires tenant timezone")
	}
	if days < 1 || days > 366 {
		return RecentSleepAvailabilityReport{}, fmt.Errorf("availability report days must be 1..366")
	}
	through, err := time.ParseInLocation("2006-01-02", throughDate, loc)
	if err != nil {
		return RecentSleepAvailabilityReport{}, fmt.Errorf("parse report through date: %w", err)
	}
	if asOf.IsZero() {
		return RecentSleepAvailabilityReport{}, fmt.Errorf("availability report requires as-of time")
	}
	report := RecentSleepAvailabilityReport{
		Version:                  RecentSleepAvailabilityReportVersion,
		EvaluationDays:           days,
		ClaimStates:              map[string]int{},
		UnknownReasons:           map[string]int{},
		TrueWithoutActionReasons: map[string]int{},
	}
	for offset := days - 1; offset >= 0; offset-- {
		date := through.AddDate(0, 0, -offset)
		wakeDate := date.Format("2006-01-02")
		evaluatedAt := time.Date(date.Year(), date.Month(), date.Day(), 18, 1, 0, 0, loc)
		if wakeDate == asOf.In(loc).Format("2006-01-02") {
			evaluatedAt = asOf
		}
		claim := EvaluateRecentSleepBelowReference(records, wakeDate, evaluatedAt, loc)
		report.ClaimStates[claim.State]++
		switch claim.State {
		case RecentSleepClaimUnknown:
			report.UnknownReasons[classifyRecentSleepUnknown(records, wakeDate, claim.Reason, loc)]++
		case RecentSleepClaimTrue:
			if claim.ActionEvent {
				report.ActionEvents++
			} else {
				report.TrueWithoutActionReasons[classifyRecentSleepActionSuppression(records, wakeDate, evaluatedAt, loc)]++
			}
		}
	}
	return report, nil
}

func classifyRecentSleepUnknown(records []CompletedNightSleep, wakeDate, reason string, loc *time.Location) string {
	if reason != "current_night_ineligible" && reason != "current_night_missing" {
		return reason
	}
	date, err := time.ParseInLocation("2006-01-02", wakeDate, loc)
	if err != nil {
		return reason
	}
	byDate := completedNightsByDate(records)
	for offset := -3; offset <= 0; offset++ {
		record, ok := byDate[date.AddDate(0, 0, offset).Format("2006-01-02")]
		if !ok {
			return "current_four_night_missing"
		}
		if record.CaptureCompleteness != NightCaptureComplete {
			return "current_four_night_no_complete_coverage"
		}
		if record.DurationAssessment != NightDurationPlausible {
			return "current_four_night_not_plausible"
		}
		if record.FinalizationState != NightFinalFinal || record.ClaimEligibility != NightClaimEligible {
			return "current_four_night_not_final_or_ineligible"
		}
	}
	return reason
}

func classifyRecentSleepActionSuppression(records []CompletedNightSleep, wakeDate string, evaluatedAt time.Time, loc *time.Location) string {
	if evaluatedAt.Before(mustNightFinalizationTime(wakeDate, loc)) {
		return "outside_evening_window"
	}
	date, err := time.ParseInLocation("2006-01-02", wakeDate, loc)
	if err != nil {
		return "unclassified"
	}
	for offset := -7; offset <= -1; offset++ {
		prior := EvaluateRecentSleepBelowReference(records, date.AddDate(0, 0, offset).Format("2006-01-02"), evaluatedAt, loc)
		if prior.State == RecentSleepClaimTrue {
			return "recent_final_true"
		}
	}
	return "unclassified"
}

func completedNightsByDate(records []CompletedNightSleep) map[string]CompletedNightSleep {
	byDate := make(map[string]CompletedNightSleep, len(records))
	for _, record := range records {
		if record.WakeDate != "" {
			byDate[record.WakeDate] = record
		}
	}
	return byDate
}
