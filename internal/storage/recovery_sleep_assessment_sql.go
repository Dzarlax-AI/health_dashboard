package storage

import (
	"context"
	"encoding/json"
	"fmt"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
	"health-receiver/internal/health"
)

var recoverySleepAssessmentSQL = `with inputs as (
    select i."Date"::date as day,i."Total" as total,i."Awake" as awake,
           i."Deep" as deep,i."REM" as rem,i."Core" as core,i."Unspecified" as unspecified
    from jsonb_to_recordset($1::jsonb) as i("Date" text,"Total" float8,"Awake" float8,"Deep" float8,"REM" float8,"Core" float8,"Unspecified" float8)
), assessed as (` + dbtmetrics.RecoverySleepAssessmentSQL("inputs") + `)
select day,eligible,eligibility_reason,efficiency,capture_class,capture_confidence,capture_reason,capture_low
from assessed order by day`

// assessRecoverySleepRows runs the shared dbt per-night formula once over the
// full local date range. Callers pass synthetic empty rows for calendar gaps.
func (s *DB) assessRecoverySleepRows(ctx context.Context, rows []health.SleepRow) (map[string]health.SleepEfficiencyResult, map[string]health.SleepCaptureConfidenceResult, error) {
	payload, err := json.Marshal(rows)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal Recovery sleep inputs: %w", err)
	}
	results, err := s.pool.Query(ctx, recoverySleepAssessmentSQL, json.RawMessage(payload))
	if err != nil {
		return nil, nil, fmt.Errorf("assess Recovery sleep inputs: %w", err)
	}
	defer results.Close()
	efficiency := make(map[string]health.SleepEfficiencyResult, len(rows))
	capture := make(map[string]health.SleepCaptureConfidenceResult, len(rows))
	for results.Next() {
		var day, eligibilityReason, captureClass, captureReason string
		var eligible, captureLow bool
		var value *float64
		var confidence float64
		if err := results.Scan(&day, &eligible, &eligibilityReason, &value, &captureClass, &confidence, &captureReason, &captureLow); err != nil {
			return nil, nil, err
		}
		efficiency[day] = health.SleepEfficiencyResult{Eligible: eligible, EligibilityReason: eligibilityReason, Efficiency: value}
		capture[day] = health.SleepCaptureConfidenceResult{Class: captureClass, Confidence: confidence, Reason: captureReason, LowConfidence: captureLow}
	}
	if err := results.Err(); err != nil {
		return nil, nil, err
	}
	if len(efficiency) != len(rows) {
		return nil, nil, fmt.Errorf("Recovery sleep assessment returned %d of %d rows", len(efficiency), len(rows))
	}
	return efficiency, capture, nil
}
