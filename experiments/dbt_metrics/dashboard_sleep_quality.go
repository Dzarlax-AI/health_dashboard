package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/dashboard_sleep_quality.sql
var dashboardSleepQualityMacro string

//go:embed macros/semantic_sleep_quality.sql
var semanticSleepQualityMacro string

var DashboardSleepQualitySQL = strings.NewReplacer(
	"{{ captures }}", "captures",
	"{{ semantic_sleep_quality('inputs') }}", strings.ReplaceAll(macroBody(semanticSleepQualityMacro), "{{ inputs }}", "inputs"),
).Replace(macroBody(dashboardSleepQualityMacro))

//go:embed macros/briefing_sections.sql
var briefingSectionsMacro string

var BriefingSectionsSQL = strings.ReplaceAll(macroBody(briefingSectionsMacro), "{{ captures }}", "section_inputs")
