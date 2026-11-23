package dbtmetrics

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed macros/hourly_avg_rows.sql
var hourlyAvgMacro string

//go:embed macros/hourly_sum_rows.sql
var hourlySumMacro string

// HourlyRowsSQL renders the dbt production projection over the writer's filtered
// hourly_points CTE. The writer supplies the date, metric, and sleep-source policy.
func HourlyRowsSQL(sum bool) string {
	macro := hourlyAvgMacro
	if sum {
		macro = hourlySumMacro
	}
	return strings.ReplaceAll(macroBody(macro), "{{ points }}", "hourly_points")
}

//go:embed macros/keep_sleep_point.sql
var keepSleepPointMacro string

// KeepSleepPointSQL keeps midnight summaries for a device when present.
// The runtime query references metric_points without an outer table alias.
func KeepSleepPointSQL() string {
	return strings.NewReplacer("{{ relation }}", "metric_points", "{{ outer_row }}", "metric_points").Replace(macroBody(keepSleepPointMacro))
}

//go:embed macros/metric_lists.sql
var metricListMacros string

// HourlySumMetricNames reads the same literal list that dbt uses for its SUM
// branch. Unknown sleep names retain the distinct live and backfill policies.
func HourlySumMetricNames() []string {
	_, list, found := strings.Cut(metricListMacros, "return(")
	literal, _, closed := strings.Cut(list, ")")
	var names []string
	if !found || !closed || json.Unmarshal([]byte(literal), &names) != nil || len(names) == 0 {
		panic("invalid dbt hourly SUM metric catalog")
	}
	return names
}

//go:embed macros/daily_source_rows.sql
var dailySourceRowsMacro string

// DailySourceRowsSQL groups persisted hours by date, metric and source. The
// caller supplies a trusted relation and scope, with values passed as parameters.
func DailySourceRowsSQL(hours, scope string) string {
	return strings.NewReplacer("{{ hours }}", hours, "{{ scope }}", scope, "{{ source_sum }}", "sum(avg_val)").Replace(macroBody(dailySourceRowsMacro))
}

//go:embed macros/daily_source_mean.sql
var dailySourceMeanMacro string

// DailySourceMeanSQL averages the per-source daily means and rounds to cache precision.
func DailySourceMeanSQL() string {
	return macroBody(dailySourceMeanMacro)
}
