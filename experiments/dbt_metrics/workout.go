package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/workout_period_totals.sql
var workoutPeriodMacro string

//go:embed macros/workout_type_totals.sql
var workoutTypeMacro string

// Substitute code-owned relations and predicates. Caller values use pgx bind parameters.
func workoutSQL(macro string, replacements ...string) string {
	return strings.NewReplacer(replacements...).Replace(macroBody(macro))
}

func WorkoutPeriodSQL(filterByName bool) string {
	scope := "s.start_time between $1 and $2"
	if filterByName {
		scope += " and s.name=$3"
	}
	return workoutSQL(workoutPeriodMacro, "{{ sessions }}", "workouts", "{{ scope }}", scope)
}

func WorkoutTypeSQL() string {
	return workoutSQL(workoutTypeMacro, "{{ sessions }}", "workouts", "{{ scope }}", "true")
}

//go:embed macros/workout_zone_seconds.sql
var workoutZoneMacro string

func WorkoutZoneSecondsSQL() string {
	return workoutSQL(workoutZoneMacro, "{{ input }}", "$1::jsonb")
}
