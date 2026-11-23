package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/sleep_duration_comparison_fields.sql
var comparisonFieldsMacro string

// SleepDurationComparisonFieldsSQL is the dbt model's scalar projection over
// the canonical night and reference inputs in the enclosing PostgreSQL query.
func SleepDurationComparisonFieldsSQL() string {
	body := strings.TrimPrefix(comparisonFieldsMacro, "{% macro sleep_duration_comparison_fields() %}")
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(body), "{% endmacro %}"))
}
