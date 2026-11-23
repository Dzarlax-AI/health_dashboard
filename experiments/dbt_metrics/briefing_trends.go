package dbtmetrics

import (
	_ "embed"
	"strings"
)

//go:embed macros/briefing_metric_trends.sql
var briefingMetricTrendsMacro string

// BriefingMetricTrendsSQL expects a series CTE keyed by profile, capture, metric and ordinal.
var BriefingMetricTrendsSQL = macroBody(briefingMetricTrendsMacro)

//go:embed macros/daily_decision_boundary.sql
var dailyDecisionMacro string

// DailyDecisionSQL evaluates the final guidance, Energy and readiness inputs after all caps.
var DailyDecisionSQL = macroBody(dailyDecisionMacro)

//go:embed macros/weekly_sleep_analysis.sql
var weeklySleepAnalysisMacro string

//go:embed macros/weekly_sleep_round.sql
var weeklySleepRoundMacro string

// WeeklySleepAnalysisSQL reads newest-first daily stage series from weekly_inputs.
var WeeklySleepAnalysisSQL = strings.ReplaceAll(macroBody(weeklySleepAnalysisMacro),
	"{{ weekly_sleep_round('value', 'scale') }}",
	strings.NewReplacer("{{ value }}", "value", "{{ scale }}", "scale").Replace(macroBody(weeklySleepRoundMacro)))
