package api

import (
	"time"

	"health-receiver/internal/health"
)

// SleepGoalRequest is a user-selected descriptive target. It is not a claim
// about biological need and must never be inferred from readiness or an AI
// response.
type SleepGoalRequest struct {
	EffectiveDate string  `json:"effective_date"`
	GoalHours     float64 `json:"goal_hours"`
}

type SleepGoalValue struct {
	EffectiveDate string  `json:"effective_date"`
	GoalHours     float64 `json:"goal_hours"`
	Version       string  `json:"version"`
}

type SleepGoalResponse struct {
	Goal *SleepGoalValue `json:"goal"`
}

type SleepDurationBalancePeriod struct {
	WakeDate           string    `json:"wake_date"`
	Start              time.Time `json:"start"`
	End                time.Time `json:"end"`
	GoalHours          float64   `json:"goal_hours"`
	TotalSleepHours    float64   `json:"total_sleep_hours"`
	DeltaHours         float64   `json:"delta_hours"`
	CaptureState       string    `json:"capture_state"`
	DurationAssessment string    `json:"duration_assessment"`
}

// SleepDurationBalanceResponse is transparent accounting relative to the
// manual goal. Null balance_hours is deliberate: it means an input period or
// effective goal is unavailable, never an estimate of zero.
type SleepDurationBalanceResponse struct {
	WakeDate          string                       `json:"wake_date"`
	WindowStartDate   string                       `json:"window_start_date"`
	CalculatedThrough time.Time                    `json:"calculated_through"`
	State             string                       `json:"state"`
	Confidence        string                       `json:"confidence"`
	IncompleteReason  string                       `json:"incomplete_reason,omitempty"`
	LastCompleteDate  string                       `json:"last_complete_date,omitempty"`
	BalanceHours      *float64                     `json:"balance_hours"`
	Periods           []SleepDurationBalancePeriod `json:"periods"`
}

func NewSleepDurationBalanceResponse(value health.SleepDurationBalance) SleepDurationBalanceResponse {
	periods := make([]SleepDurationBalancePeriod, 0, len(value.Periods))
	for _, period := range value.Periods {
		periods = append(periods, SleepDurationBalancePeriod{
			WakeDate: period.WakeDate, Start: period.Start, End: period.End, GoalHours: period.GoalHours,
			TotalSleepHours: period.TotalSleepHours, DeltaHours: period.DeltaHours,
			CaptureState: period.CaptureState, DurationAssessment: period.DurationAssessment,
		})
	}
	return SleepDurationBalanceResponse{
		WakeDate: value.WakeDate, WindowStartDate: value.WindowStartDate, CalculatedThrough: value.CalculatedThrough,
		State: value.State, Confidence: value.Confidence, IncompleteReason: value.IncompleteReason,
		LastCompleteDate: value.LastCompleteDate, BalanceHours: value.BalanceHours, Periods: periods,
	}
}
