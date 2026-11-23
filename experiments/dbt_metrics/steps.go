package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/step_source_order.sql
var stepSourceOrderMacro string

// StepSourceOrderSQL ranks competing readings within an hour. Expressions
// name code-owned SQL columns; the whole winning row supplies chart extrema.
func StepSourceOrderSQL(source, value string) string {
	return strings.NewReplacer("{{ source }}", source, "{{ value }}", value).Replace(macroBody(stepSourceOrderMacro))
}

//go:embed macros/daily_step_totals.sql
var dailyStepTotalsMacro string

// DailyStepTotalsSQL sums the selected source hours for each stored date.
func DailyStepTotalsSQL(chosen string) string {
	return strings.ReplaceAll(macroBody(dailyStepTotalsMacro), "{{ chosen }}", chosen)
}

//go:embed macros/incomplete_step_hours.sql
var incompleteStepHoursMacro string

// IncompleteStepHoursSQL identifies wall hours whose cached inputs cannot be reconstructed.
func IncompleteStepHoursSQL(points, cached string) string {
	return strings.NewReplacer("{{ points }}", points, "{{ cached }}", cached, "{{ ambiguous_step_hours(points) }}", AmbiguousStepHoursSQL(points)).Replace(macroBody(incompleteStepHoursMacro))
}

//go:embed macros/physical_step_hours.sql
var physicalStepHoursMacro string

// PhysicalStepHoursSQL returns one winning source per observed physical hour.
func PhysicalStepHoursSQL(points, cached string) string {
	return strings.NewReplacer("{{ step_source_candidates(points,cached) }}", PhysicalStepSourceHoursSQL(points, cached),
		"{{ step_source_order('source','avg_val') }}", StepSourceOrderSQL("source", "avg_val")).Replace(macroBody(physicalStepHoursMacro))
}

//go:embed macros/step_source_candidates.sql
var stepSourceCandidatesMacro string

// PhysicalStepSourceHoursSQL retains each source cell before source priority is applied.
func PhysicalStepSourceHoursSQL(points, cached string) string {
	return strings.NewReplacer("{{ points }}", points, "{{ cached }}", cached,
		"{{ ambiguous_step_hours('step_input') }}", AmbiguousStepHoursSQL("step_input"),
		"{{ reconciled_step_alias_cells('step_input','frozen_cache') }}", ReconciledStepAliasCellsSQL("step_input", "frozen_cache"),
		"{{ step_wall_rows('uncached_wall_points') }}", StepWallRowsSQL("uncached_wall_points"),
		"{{ incomplete_step_hours('step_input','wall_cache') }}", IncompleteStepHoursSQL("step_input", "wall_cache")).Replace(macroBody(stepSourceCandidatesMacro))
}

//go:embed macros/step_instant.sql
var stepInstantMacro string

// StepInstantSQL returns a timezone-independent instant only for complete valid timestamps.
func StepInstantSQL(dateExpr string) string {
	return strings.ReplaceAll(macroBody(stepInstantMacro), "{{ date_expr }}", dateExpr)
}

//go:embed macros/step_point_inputs.sql
var stepPointInputsMacro string

// StepPointInputsSQL resolves input grain and exact-record precedence before aggregation.
func StepPointInputsSQL(points, receipts, scope string) string {
	return strings.NewReplacer("{{ points }}", points, "{{ receipts }}", receipts, "{{ scope }}", scope, "{{ step_instant('e.date') }}", StepInstantSQL("e.date")).Replace(macroBody(stepPointInputsMacro))
}

//go:embed macros/step_wall_rows.sql
var stepWallRowsMacro string

// StepWallRowsSQL produces source cells for the offset-free compatibility cache.
func StepWallRowsSQL(points string) string {
	sumRows := strings.ReplaceAll(macroBody(hourlySumMacro), "{{ points }}", "counted_points")
	return strings.NewReplacer("{{ points }}", points, "{{ hourly_sum_rows('counted_points') }}", sumRows).Replace(macroBody(stepWallRowsMacro))
}

//go:embed macros/reconciled_step_alias_cells.sql
var reconciledStepAliasCellsMacro string

// ReconciledStepAliasCellsSQL corrects proven source cells even when another
// source in the same wall hour needs legacy fallback. NULL withdraws a cell.
func ReconciledStepAliasCellsSQL(points, cached string) string {
	return strings.NewReplacer("{{ points }}", points, "{{ cached }}", cached,
		"{{ step_wall_rows('original_points') }}", StepWallRowsSQL("original_points"),
		"{{ step_wall_rows('cell_points') }}", StepWallRowsSQL("cell_points")).Replace(macroBody(reconciledStepAliasCellsMacro))
}
