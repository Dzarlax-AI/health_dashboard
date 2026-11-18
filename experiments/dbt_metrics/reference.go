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
