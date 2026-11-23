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
	return dailyStepTotalsSQL(chosen, false)
}

func dailyStepTotalsSQL(chosen string, ordered bool) string {
	return strings.ReplaceAll(stepMacroBody(dailyStepTotalsMacro, ordered), "{{ chosen }}", chosen)
}

//go:embed macros/incomplete_step_hours.sql
var incompleteStepHoursMacro string

// IncompleteStepHoursSQL identifies wall hours whose cached inputs cannot be reconstructed.
func IncompleteStepHoursSQL(points, cached string) string {
	return incompleteStepHoursSQL(points, cached, false)
}

func incompleteStepHoursSQL(points, cached string, ordered bool) string {
	return strings.NewReplacer("{{ points }}", points, "{{ cached }}", cached, "{{ ambiguous_step_hours(points) }}", AmbiguousStepHoursSQL(points)).Replace(stepMacroBody(incompleteStepHoursMacro, ordered))
}

//go:embed macros/physical_step_hours.sql
var physicalStepHoursMacro string

// PhysicalStepHoursSQL returns one winning source per observed physical hour.
func PhysicalStepHoursSQL(points, cached string) string {
	return physicalStepHoursSQL(points, cached, false)
}

func physicalStepHoursSQL(points, cached string, ordered bool) string {
	return strings.NewReplacer("{{ step_source_candidates(points,cached, ordered_steps) }}", physicalStepSourceHoursSQL(points, cached, ordered),
		"{{ step_source_order('source','avg_val') }}", StepSourceOrderSQL("source", "avg_val")).Replace(stepMacroBody(physicalStepHoursMacro, ordered))
}

//go:embed macros/step_source_candidates.sql
var stepSourceCandidatesMacro string

// PhysicalStepSourceHoursSQL retains each source cell before source priority is applied.
func PhysicalStepSourceHoursSQL(points, cached string) string {
	return physicalStepSourceHoursSQL(points, cached, false)
}

func physicalStepSourceHoursSQL(points, cached string, ordered bool) string {
	return strings.NewReplacer("{{ points }}", points, "{{ cached }}", cached,
		"{{ ambiguous_step_hours('step_input') }}", AmbiguousStepHoursSQL("step_input"),
		"{{ reconciled_step_alias_cells('step_input','frozen_cache', ordered_steps) }}", reconciledStepAliasCellsSQL("step_input", "frozen_cache", ordered),
		"{{ step_wall_rows('uncached_wall_points', ordered_steps) }}", stepWallRowsSQL("uncached_wall_points", ordered),
		"{{ incomplete_step_hours('step_input','wall_cache', ordered_steps) }}", incompleteStepHoursSQL("step_input", "wall_cache", ordered)).Replace(stepMacroBody(stepSourceCandidatesMacro, ordered))
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
	// Preserve the historical v6 writer definition and its maintenance identity.
	return stepPointInputsSQL(points, receipts, scope, "b.candidate_day::text")
}

// ServingStepPointInputsSQL matches the dbt reader in every PostgreSQL DateStyle.
func ServingStepPointInputsSQL(points, receipts, scope string) string {
	return stepPointInputsSQL(points, receipts, scope, "to_char(b.candidate_day,'YYYY-MM-DD')")
}

func stepPointInputsSQL(points, receipts, scope, neighborKey string) string {
	return strings.NewReplacer("{{ neighbor_key }}", neighborKey, "{{ points }}", points, "{{ receipts }}", receipts, "{{ scope }}", scope, "{{ step_instant('e.date') }}", StepInstantSQL("e.date")).Replace(macroBody(stepPointInputsMacro))
}

//go:embed macros/step_wall_rows.sql
var stepWallRowsMacro string

// StepWallRowsSQL produces source cells for the offset-free compatibility cache.
func StepWallRowsSQL(points string) string {
	return stepWallRowsSQL(points, false)
}

// ServingStepWallRowsSQL rebuilds a source cell released by a verified receipt.
func ServingStepWallRowsSQL(points string) string {
	return stepWallRowsSQL(points, true)
}

func stepWallRowsSQL(points string, ordered bool) string {
	sumRows := strings.ReplaceAll(stepMacroBody(hourlySumMacro, ordered), "{{ points }}", "counted_points")
	return strings.NewReplacer("{{ points }}", points, "{{ hourly_sum_rows('counted_points', ordered_steps) }}", sumRows).Replace(stepMacroBody(stepWallRowsMacro, ordered))
}

//go:embed macros/reconciled_step_alias_cells.sql
var reconciledStepAliasCellsMacro string

// ReconciledStepAliasCellsSQL corrects proven source cells even when another
// source in the same wall hour needs legacy fallback. NULL withdraws a cell.
func ReconciledStepAliasCellsSQL(points, cached string) string {
	return reconciledStepAliasCellsSQL(points, cached, false)
}

func reconciledStepAliasCellsSQL(points, cached string, ordered bool) string {
	return strings.NewReplacer("{{ points }}", points, "{{ cached }}", cached,
		"{{ step_wall_rows('original_points', ordered_steps) }}", stepWallRowsSQL("original_points", ordered),
		"{{ step_wall_rows('cell_points', ordered_steps) }}", stepWallRowsSQL("cell_points", ordered)).Replace(stepMacroBody(reconciledStepAliasCellsMacro, ordered))
}

//go:embed macros/step_capture_signature.sql
var stepCaptureSignatureMacro string

//go:embed macros/proven_step_edits.sql
var provenStepEditsMacro string

// ServingProvenStepEditsSQL identifies an edit whose previous raw capture can
// reconstruct the published hour and whose previous observations still exist.
func ServingProvenStepEditsSQL(points, cached, previous, scope string) string {
	signature := strings.ReplaceAll(macroBody(stepCaptureSignatureMacro), "{{ points }}", points)
	return strings.NewReplacer("{{ cached }}", cached, "{{ scope }}", scope,
		"{{ incoming }}", "metric_points", "{{ previous }}", previous,
		"{{ full_snapshot }}", "true", "{{ step_capture_signature(points) }}", signature,
		"{{ incomplete_step_hours(points,'unproven', ordered_steps) }}", incompleteStepHoursSQL(points, "unproven", true),
	).Replace(macroBody(provenStepEditsMacro))
}

// OrderedStepTotalsSQL is the worker's chronological formula, also used for
// raw reads of the enrolled profile. Legacy writers retain their original SQL.
func OrderedStepTotalsSQL(points, cached string) string {
	return dailyStepTotalsSQL("("+physicalStepHoursSQL(points, cached, true)+") selected", true)
}

// Step macros use this one boolean conditional to opt into chronological
// accumulation. Keeping the legacy branch byte-identical preserves the Go
// cache identity and avoids a fleet-wide historical rebuild.
func stepMacroBody(template string, ordered bool) string {
	body := macroBody(template)
	const start = "{% if ordered_steps %}"
	if !strings.Contains(body, start) {
		return body
	}
	var out strings.Builder
	for {
		prefix, rest, found := strings.Cut(body, start)
		out.WriteString(prefix)
		if !found {
			break
		}
		choices, remaining, closed := strings.Cut(rest, "{% endif %}")
		if !closed {
			panic("unterminated step order conditional")
		}
		enabled, legacy, _ := strings.Cut(choices, "{% else %}")
		if ordered {
			out.WriteString(enabled)
		} else {
			out.WriteString(legacy)
		}
		body = remaining
	}
	return out.String()
}
