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
	body := strings.TrimPrefix(shadowSleepDailyMacro, "{% macro shadow_sleep_daily_v1(candidates, scope) %}")
	body = strings.TrimSuffix(strings.TrimSpace(body), "{% endmacro %}")
	return strings.TrimSpace(strings.NewReplacer(
		"{{ candidates }}", "sleep_shadow.candidate",
		"{{ scope }}", scope,
	).Replace(body))
}
