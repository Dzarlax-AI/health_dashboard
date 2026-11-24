// Package dbtmetrics exposes the SQL shared by the dbt pilot and Health.
package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/sleep_duration_reference_v1.sql
var sleepReferenceMacro string

// SleepDurationReferenceSQL renders the model body. relation must be a trusted,
// quoted SQL identifier for the canonical nights table.
func SleepDurationReferenceSQL(relation string) string {
	body := strings.TrimPrefix(sleepReferenceMacro, "{% macro sleep_duration_reference_v1(nights) %}")
	body = strings.TrimSuffix(strings.TrimSpace(body), "{% endmacro %}")
	return strings.TrimSpace(strings.ReplaceAll(body, "{{ nights }}", relation))
}

//go:embed macros/shadow_sleep_daily_v1.sql
var shadowSleepDailyMacro string

// ShadowSleepDailySQL renders the daily sleep model for the shadow repository.
// scope must be a trusted SQL predicate, with any request values passed as bind parameters.
func ShadowSleepDailySQL(scope string) string {
	return SleepDailySQL("sleep_shadow.candidate", scope)
}

// SleepDailySQL renders the shared model using trusted relation and predicate SQL.
func SleepDailySQL(relation, scope string) string {
	body := strings.TrimPrefix(shadowSleepDailyMacro, "{% macro shadow_sleep_daily_v1(candidates, scope) %}")
	body = strings.TrimSuffix(strings.TrimSpace(body), "{% endmacro %}")
	return strings.TrimSpace(strings.NewReplacer(
		"{{ candidates }}", relation,
		"{{ scope }}", scope,
	).Replace(body))
}

//go:embed macros/legacy_sleep_selection_v1.sql
var legacySleepSelectionMacro string

// LegacySleepSelectionSQL renders the dbt selection CTEs with trusted catalog SQL.
func LegacySleepSelectionSQL(relation, priority, minHours, divergence, traditional, traditionalCount, coarse, coarseCount string) string {
	body := strings.TrimPrefix(legacySleepSelectionMacro, "{% macro legacy_sleep_selection_v1(relation, priority, min_hours, divergence, traditional, traditional_count, coarse, coarse_count) %}")
	body = strings.TrimSuffix(strings.TrimSpace(body), "{% endmacro %}")
	return strings.TrimSpace(strings.NewReplacer(
		"{{ relation }}", relation,
		"{{ priority }}", priority,
		"{{ min_hours }}", minHours,
		"{{ divergence }}", divergence,
		"{{ traditional }}", traditional,
		"{{ traditional_count }}", traditionalCount,
		"{{ coarse }}", coarse,
		"{{ coarse_count }}", coarseCount,
	).Replace(body))
}

//go:embed macros/sleep_balance_period_v1.sql
var sleepBalancePeriodMacro string

// SleepBalancePeriodSQL renders the canonical balance period calculation from
// the same macro used by the dbt model. Relation arguments must be trusted SQL
// identifiers supplied by the caller.
func SleepBalancePeriodSQL(captures, goals, coverage, episodes string) string {
	body := macroBody(sleepBalancePeriodMacro)
	return strings.NewReplacer(
		"{{ captures }}", captures,
		"{{ goals }}", goals,
		"{{ coverage }}", coverage,
		"{{ episodes }}", episodes,
	).Replace(body)
}

//go:embed macros/sleep_balance_window_v1.sql
var sleepBalanceWindowMacro string

// SleepBalanceWindowSQL renders the canonical balance window summary from the
// same macro used by the dbt model. periods must be a trusted SQL relation.
func SleepBalanceWindowSQL(periods string) string {
	body := macroBody(sleepBalanceWindowMacro)
	return strings.ReplaceAll(body, "{{ periods }}", periods)
}

//go:embed macros/completed_night_sleep_v1.sql
var completedNightSleepMacro string

// CompletedNightSleepSQL renders the canonical completed-night state
// calculation shared by dbt and transactional production writers.
func CompletedNightSleepSQL(inputs string) string {
	body := macroBody(completedNightSleepMacro)
	body = strings.ReplaceAll(body, "{{ completed_night_duration_assessment_v1('duration_hours') }}", CompletedNightDurationAssessmentSQL("duration_hours"))
	return strings.ReplaceAll(body, "{{ inputs }}", inputs)
}

//go:embed macros/completed_night_duration_assessment_v1.sql
var completedNightDurationAssessmentMacro string

// CompletedNightDurationAssessmentSQL renders the canonical duration class
// used by both source selection and completed-night persistence.
func CompletedNightDurationAssessmentSQL(hours string) string {
	body := macroBody(completedNightDurationAssessmentMacro)
	return strings.ReplaceAll(body, "{{ hours }}", hours)
}
