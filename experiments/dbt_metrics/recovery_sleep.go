package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/recovery_sleep_assessment_v1.sql
var recoverySleepAssessmentMacro string

// RecoverySleepAssessmentSQL renders the per-night Recovery classification
// shared by dbt replay and transactional Go storage adapters.
func RecoverySleepAssessmentSQL(inputs string) string {
	body := macroBody(recoverySleepAssessmentMacro)
	return strings.ReplaceAll(body, "{{ inputs }}", inputs)
}
