package storage

import (
	"fmt"
	"strconv"
	"strings"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
)

// aggregationMetricSpec maps a daily_scores column to its hourly metric.
type aggregationMetricSpec struct {
	column string
	metric string
}

type sourcePriorityRule struct {
	name     string
	patterns []string
}

// These ordering rules intentionally encode the existing sleep and SUM
// source-selection policies. Their literal values also feed the contract hash.
const (
	sleepCrossValidationMinHours   = 1.0
	sleepCrossValidationDivergence = 1.4
)

var (
	sleepSourcePriority = []sourcePriorityRule{
		{name: "Apple Watch", patterns: []string{"Ultra", "Apple Watch"}},
		{name: "RingConn", patterns: []string{"RingConn"}},
	}
	sumSourcePriority = []sourcePriorityRule{
		{name: "Apple Watch", patterns: []string{"Ultra", "Apple Watch"}},
		{name: "iPhone", patterns: []string{"iPhone"}},
	}
	sleepMetricNames        = []string{"sleep_total", "sleep_deep", "sleep_rem", "sleep_core", "sleep_awake", "sleep_unspecified"}
	sleepTraditionalMetrics = []string{"sleep_total", "sleep_deep", "sleep_rem", "sleep_core", "sleep_awake"}
	sleepCoarseMetrics      = []string{"sleep_total", "sleep_unspecified"}
	dailyMetricSpecs        = []aggregationMetricSpec{
		{column: "hrv_avg", metric: "heart_rate_variability"},
		{column: "rhr_avg", metric: "resting_heart_rate"},
		{column: "steps", metric: "step_count"},
		{column: "calories", metric: "active_energy"},
		{column: "exercise_min", metric: "apple_exercise_time"},
		{column: "spo2_avg", metric: "blood_oxygen_saturation"},
		{column: "vo2_avg", metric: "vo2_max"},
		{column: "resp_avg", metric: "respiratory_rate"},
	}
)

// SumMetrics is the canonical set of metrics that should be SUMmed within a
// bucket. It remains exported for MCP and other package compatibility.
// sleep_unspecified is coarse asleep time and remains mutually exclusive with
// the staged sleep metrics per source. night_sleep_total and nap_total retain
// SUM semantics but are not columns in daily_scores.
var SumMetrics = func() map[string]bool {
	metrics := dbtmetrics.HourlySumMetricNames()
	out := make(map[string]bool, len(metrics))
	for _, metric := range metrics {
		out[metric] = true
	}
	return out
}()

func isDailyAverageMetric(metric string) bool {
	for _, spec := range dailyMetricSpecs {
		if spec.metric == metric {
			return !SumMetrics[metric]
		}
	}
	return false
}

func allowedDailyMetricColumn(column string) bool {
	for _, spec := range dailyMetricSpecs {
		if spec.column == column {
			return true
		}
	}
	return false
}

func sourcePriorityCondition(alias string, rule sourcePriorityRule) string {
	conditions := make([]string, 0, len(rule.patterns))
	for _, pattern := range rule.patterns {
		conditions = append(conditions, alias+" LIKE '%"+pattern+"%'")
	}
	return strings.Join(conditions, " OR ")
}

func sleepSourcePriorityCaseExpr(alias string) string {
	var cases []string
	for i, rule := range sleepSourcePriority {
		cases = append(cases, "WHEN "+sourcePriorityCondition(alias, rule)+fmt.Sprintf(" THEN %d", i))
	}
	cases = append(cases, fmt.Sprintf("ELSE %d END", len(sleepSourcePriority)))
	return "CASE " + strings.Join(cases, " ")
}

func sqlStringList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "'"+value+"'")
	}
	return strings.Join(quoted, ",")
}

// sqlPolicyNumber preserves the full constant value and the existing decimal
// literal type for whole-valued thresholds (1.0 rather than integer 1).
func sqlPolicyNumber(value float64) string {
	literal := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.Contains(literal, ".") {
		literal += ".0"
	}
	return literal
}
