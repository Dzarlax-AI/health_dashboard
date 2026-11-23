package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
)

type recoverySleepDay struct {
	Day           string   `json:"day"`
	EligibleHours *float64 `json:"eligible_hours"`
	Class         string   `json:"class"`
	Confidence    float64  `json:"confidence"`
	Low           bool     `json:"low"`
}

func recoverySleepDays(t time.Time, byDate map[string]health.SleepRow, eff map[string]health.SleepEfficiencyResult, captures map[string]health.SleepCaptureConfidenceResult) []recoverySleepDay {
	days := make([]recoverySleepDay, 0, 7)
	for i := range 7 {
		day := t.AddDate(0, 0, -i).Format(isoDate)
		capture := sleepCaptureForDate(captures, day)
		assessment := recoverySleepDay{Day: day, Class: capture.Class, Confidence: capture.Confidence, Low: capture.LowConfidence}
		if eff[day].Eligible {
			assessment.EligibleHours = byDate[day].Total
		}
		days = append(days, assessment)
	}
	return days
}

var recoverySleepFeaturesSQL = `with selected as (
    select ''::text as case_id, $1::text::date as as_of_date, nullif($2::text,'')::date as epoch_start, $3::jsonb as days
), result as (` + dbtmetrics.ChipRecoverySleepSQL("selected") + `)
select debt_hours, mean_confidence, low_days, class_counts from result`

func (s *DB) recoveryFeatureContext(ctx context.Context, t time.Time, epochStart string, byDate map[string]health.SleepRow, eff map[string]health.SleepEfficiencyResult, captures map[string]health.SleepCaptureConfidenceResult, arch SleepArchitectureFeatureFields) (recoveryFeatures, error) {
	capture := sleepCaptureForDate(captures, t.Format(isoDate))
	out := recoveryFeatures{SleepArchitectureFeatureFields: arch, SleepCaptureClass: capture.Class, SleepCaptureConfidence: ptrFloat(capture.Confidence), SleepCaptureReason: capture.Reason, SleepCaptureLow: capture.LowConfidence}
	payload, err := json.Marshal(recoverySleepDays(t, byDate, eff, captures))
	if err != nil {
		return out, err
	}
	err = s.pool.QueryRow(ctx, recoverySleepFeaturesSQL, t.Format(isoDate), epochStart, json.RawMessage(payload)).Scan(&out.SleepDebt7dHours, &out.SleepCaptureMeanConfidence7d, &out.SleepCaptureLowDays7d, &out.SleepCaptureClassCounts7d)
	if err != nil {
		return out, fmt.Errorf("recovery sleep features %s: %w", t.Format(isoDate), err)
	}
	return out, nil
}
