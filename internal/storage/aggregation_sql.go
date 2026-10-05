package storage

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

//go:embed sql/aggregates/*.sql
var aggregateSQLFiles embed.FS
var aggregateSQLMarker = regexp.MustCompile(`\{\{([A-Z0-9_]+)\}\}`)

func aggregateSQL(name string) string {
	b, err := aggregateSQLFiles.ReadFile("sql/aggregates/" + name + ".sql")
	if err != nil {
		panic(fmt.Sprintf("read embedded aggregate SQL %q: %v", name, err))
	}
	return string(b)
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
const AggregateContractVersion = "1"

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
		"legacy-sum-rank="+legacySumSourceRankExpr(),
		"sleep-priority-case="+sleepSourcePriorityCaseExpr("source"),
	)
	entries, err := aggregateSQLFiles.ReadDir("sql/aggregates")
	if err != nil {
		panic(fmt.Sprintf("list embedded aggregate SQL: %v", err))
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := aggregateSQLFiles.ReadFile("sql/aggregates/" + entry.Name())
		if err != nil {
			panic(fmt.Sprintf("read embedded aggregate SQL %q: %v", entry.Name(), err))
		}
		parts = append(parts, "sql="+entry.Name()+"\n"+string(data))
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
