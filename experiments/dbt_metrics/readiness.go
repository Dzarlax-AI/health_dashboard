package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/readiness_score_v1.sql
var readinessScoreMacro string

//go:embed macros/readiness_hrv_math_v1.sql
var readinessHRVMathMacro string

// ReadinessScoreSQL runs on the input relation supplied by the reader.
func ReadinessScoreSQL(captures string) string {
	body := macroBody(readinessScoreMacro)
	// Bounded reader inputs are already filtered. Evaluate weighted and raw
	// scores once per date; the multi-profile dbt view keeps predicate pushdown.
	return strings.NewReplacer("{{ captures }}", captures, "{{ materialization }}", "materialized").Replace(body)
}

// ReadinessHRVMathSQL scores the HRV observations selected for this reader.
func ReadinessHRVMathSQL(eligibility string) string {
	body := macroBody(readinessHRVMathMacro)
	return strings.ReplaceAll(body, "{{ eligibility }}", eligibility)
}

//go:embed macros/readiness_effective_eligibility_v1.sql
var readinessEffectiveEligibilityMacro string

// ReadinessEffectiveEligibilitySQL projects the immutable ordered score inputs.
func ReadinessEffectiveEligibilitySQL(inputs string) string {
	return strings.ReplaceAll(macroBody(readinessEffectiveEligibilityMacro), "{{ inputs }}", inputs)
}
