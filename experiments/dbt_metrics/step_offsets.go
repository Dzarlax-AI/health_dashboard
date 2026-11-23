package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/ambiguous_step_hours.sql
var ambiguousStepHoursMacro string

// AmbiguousStepHoursSQL identifies wall hours that cannot be split by offset.
func AmbiguousStepHoursSQL(points string) string {
	return strings.ReplaceAll(macroBody(ambiguousStepHoursMacro), "{{ points }}", points)
}
