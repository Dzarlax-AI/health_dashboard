package storage

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strings"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
)

//go:embed sql/aggregates/*.sql
var aggregateSQLFiles embed.FS
var aggregateSQLMarker = regexp.MustCompile(`\{\{([A-Z0-9_]+)\}\}`)

// The withdrawal statement has already resolved aliases for every point on
// its scoped dates. Reuse those decisions when recomputing a touched day.
const touchedStepInputs = `(select p.date,p.source,p.qty,p.quality,p.grain,p.stored_key,p.counted
	from step_points p join touched t on substring(p.date,1,10)=t.day) inputs`

var aggregateProjections = strings.NewReplacer(
	"{{SURVIVING_STEP_TOTALS}}", dbtmetrics.DailyStepTotalsSQL("("+dbtmetrics.PhysicalStepHoursSQL(touchedStepInputs, "surviving")+") selected"),
	"{{DAILY_STEP_TOTALS}}", physicalStepTotalsSQL("substring(date,1,10)=$1", "hourly_metrics", "substring(hour,1,10)=$1"),
	"{{HISTORICAL_STEP_TOTALS}}", physicalStepTotalsSQL("$1::text='step_count' {{POINT_SCOPE}}", "hourly_metrics", "metric_name=$1 {{FROM_CLAUSE}}"),
	"{{WITHDRAWAL_STEP_INPUTS}}", dbtmetrics.StepPointInputsSQL("metric_points", "health_records", "true {{DATE_SCOPE}}"),
	"{{LIVE_STEP_INPUTS}}", dbtmetrics.StepPointInputsSQL("metric_points", "health_records", "substring(p.date,1,10)=$1"),
	"{{LEGACY_STEP_INPUTS}}", dbtmetrics.StepPointInputsSQL("metric_points", "health_records", "$1::text='step_count' {{FROM_CLAUSE}}"),
	"{{STEP_WALL_ROWS}}", dbtmetrics.StepWallRowsSQL("step_points"),
	"{{RECONCILED_STEP_ALIAS_CELLS}}", dbtmetrics.ReconciledStepAliasCellsSQL("step_points", "cached"),
	"{{INCOMPLETE_STEP_HOURS}}", dbtmetrics.IncompleteStepHoursSQL("step_points", "cached"),
	"{{DAILY_SLEEP_SOURCES}}", dbtmetrics.DailySourceRowsSQL("hourly_metrics", "metric_name IN ({{SLEEP_METRICS}}) {{FROM_CLAUSE}}"),
	"{{BACKFILL_SLEEP_SELECTION}}", dbtmetrics.LegacySleepSelectionSQL("per_source", "{{SLEEP_PRIORITY_CASE}}", "{{SLEEP_MIN_HOURS}}", "{{SLEEP_DIVERGENCE}}", "{{SLEEP_TRADITIONAL_METRICS}}", "5", "{{SLEEP_COARSE_METRICS}}", "2"),
	"{{DAILY_SLEEP_CANDIDATES}}", dbtmetrics.DailyCandidatesSQL("false", "false"),
	"{{DAILY_RETAINED_SLEEP}}", dbtmetrics.DailyRetainedSleepSQL("excluded", "daily_scores"),
	"{{DAILY_PREFERRED_SUM}}", dbtmetrics.DailyPreferredSumSQL(sourcePriorityCondition("source", sumSourcePriority[0]), sourcePriorityCondition("source", sumSourcePriority[1])),
	"{{DAILY_CANDIDATES}}", dbtmetrics.DailyCandidatesSQL("{{SUM_WATCH_CONDITION}}", "{{SUM_IPHONE_CONDITION}}"),
	"{{DAILY_RETAINED}}", dbtmetrics.DailyRetainedValuesSQL("excluded", "daily_scores", "false"),
	"{{DAILY_SOURCE_MEAN}}", dbtmetrics.DailySourceMeanSQL(),
	"{{DAILY_DATE_SOURCES}}", dbtmetrics.DailySourceRowsSQL("hourly_metrics", "SUBSTRING(hour,1,10) = $1"),
	"{{DAILY_METRIC_SOURCES}}", dbtmetrics.DailySourceRowsSQL("hourly_metrics", "metric_name = $1 {{FROM_CLAUSE}}"),
	"{{HOURLY_AVG_ROWS}}", dbtmetrics.HourlyRowsSQL(false),
	"{{HOURLY_SUM_ROWS}}", dbtmetrics.HourlyRowsSQL(true),
)

func aggregateSQL(name string) string {
	b, err := aggregateSQLFiles.ReadFile("sql/aggregates/" + name + ".sql")
	if err != nil {
		panic(fmt.Sprintf("read embedded aggregate SQL %q: %v", name, err))
	}
	return aggregateProjections.Replace(string(b))
}

func aggregateSQLWith(name string, replacements map[string]string) string {
	q := aggregateSQL(name)
	for marker, value := range replacements {
		needle := "{{" + marker + "}}"
		if !strings.Contains(q, needle) {
			panic(fmt.Sprintf("aggregate SQL %q does not use marker %q", name, marker))
		}
		q = strings.ReplaceAll(q, needle, value)
	}
	if match := aggregateSQLMarker.FindStringSubmatch(q); match != nil {
		panic(fmt.Sprintf("aggregate SQL %q has unresolved marker %q", name, match[1]))
	}
	return q
}

// AggregateContractVersion identifies the current aggregation policy and SQL
// contract. It is for reports and comparisons; cache rows do not store it.
const AggregateContractVersion = "6"

// AggregateContractChecksum is stable for a given policy catalog and embedded
// SQL set, independent of filesystem ordering and map iteration.
func AggregateContractChecksum() string {
	var parts []string
	parts = append(parts, "version="+AggregateContractVersion)
	metricNames := make([]string, 0, len(SumMetrics))
	for name := range SumMetrics {
		metricNames = append(metricNames, name)
	}
	sort.Strings(metricNames)
	for _, name := range metricNames {
		parts = append(parts, fmt.Sprintf("sum=%s:%t", name, SumMetrics[name]))
	}
	for _, spec := range dailyMetricSpecs {
		parts = append(parts, "daily="+spec.column+":"+spec.metric)
	}
	parts = append(parts,
		fmt.Sprintf("sleep-min-hours=%g", sleepCrossValidationMinHours),
		fmt.Sprintf("sleep-divergence=%g", sleepCrossValidationDivergence),
		"sleep-priority="+sourcePriorityFingerprint(sleepSourcePriority),
		"sum-priority="+sourcePriorityFingerprint(sumSourcePriority),
		"sleep-metrics="+strings.Join(sleepMetricNames, ","),
		"sleep-traditional="+strings.Join(sleepTraditionalMetrics, ","),
		"sleep-coarse="+strings.Join(sleepCoarseMetrics, ","),
		"sleep-dedup="+sleepDedupClause("sleep_total"),
		"sleep-pick-value="+sleepCrossValidationPickExpr("sum_val"),
		"sleep-pick-source="+sleepCrossValidationPickSourceExpr("source_totals", "sum_val"),
		"preferred-source="+preferredSourceSQL,
		"preferred-sleep-source="+preferredSleepSourceSQL,
		"sleep-priority-case="+sleepSourcePriorityCaseExpr("source"),
	)
	entries, err := aggregateSQLFiles.ReadDir("sql/aggregates")
	if err != nil {
		panic(fmt.Sprintf("list embedded aggregate SQL: %v", err))
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "live_daily_go_sleep.sql" {
			continue
		}
		data := aggregateSQL(strings.TrimSuffix(entry.Name(), ".sql"))
		if entry.Name() == "live_daily.sql" {
			// Fingerprint the default query as a single template, before rendering
			// the Go policy. Splitting the template and adding a parity-only opt-in
			// writer do not invalidate existing cache maintenance checkpoints.
			data = strings.Replace(data, "{{SLEEP_SELECTION_CTES}}",
				strings.TrimSuffix(aggregateSQL("live_daily_go_sleep"), "\n"), 1)
		}
		parts = append(parts, "sql="+entry.Name()+"\n"+data)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(strings.Join(parts, "\n"))))
}

func sourcePriorityFingerprint(rules []sourcePriorityRule) string {
	var entries []string
	for _, rule := range rules {
		entries = append(entries, rule.name+":"+strings.Join(rule.patterns, ","))
	}
	return strings.Join(entries, ";")
}
