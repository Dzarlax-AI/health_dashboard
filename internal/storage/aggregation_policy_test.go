package storage

import (
	"strings"
	"testing"
)

func TestSQLPolicyNumberPreservesPrecision(t *testing.T) {
	for value, want := range map[float64]string{1: "1.0", 1.4: "1.4", 1.45: "1.45", 0.125: "0.125"} {
		if got := sqlPolicyNumber(value); got != want {
			t.Errorf("sqlPolicyNumber(%g) = %q, want %q", value, got, want)
		}
	}
}

func TestAggregateContractChecksumIsStableAndVersioned(t *testing.T) {
	if AggregateContractVersion == "" {
		t.Fatal("aggregate contract version must be set")
	}
	first := AggregateContractChecksum()
	if !strings.HasPrefix(first, "sha256:") || len(first) != len("sha256:")+64 {
		t.Fatalf("unexpected aggregate contract checksum %q", first)
	}
	if second := AggregateContractChecksum(); first != second {
		t.Fatalf("aggregate contract checksum changed between calls: %q != %q", first, second)
	}
}

func TestAggregateContractChecksumTracksClassification(t *testing.T) {
	before := AggregateContractChecksum()
	original := SumMetrics["sleep_unspecified"]
	SumMetrics["sleep_unspecified"] = !original
	t.Cleanup(func() { SumMetrics["sleep_unspecified"] = original })
	if after := AggregateContractChecksum(); after == before {
		t.Fatal("aggregate checksum did not change when SUM classification changed")
	}
}

func TestAggregationCatalogMatchesLegacyPublicSurface(t *testing.T) {
	if len(dailyMetricSpecs) != 8 {
		t.Fatalf("daily metric catalog has %d entries, want 8", len(dailyMetricSpecs))
	}
	for _, spec := range dailyMetricSpecs {
		if !allowedDailyMetricColumn(spec.column) {
			t.Errorf("daily metric column %q is not allowlisted", spec.column)
		}
	}
	if !SumMetrics["sleep_unspecified"] || !SumMetrics["night_sleep_total"] || !SumMetrics["nap_total"] {
		t.Fatal("canonical SUM catalog lost a sleep metric")
	}
	if len(sleepMetricNames) != 6 || len(sleepTraditionalMetrics) != 5 || len(sleepCoarseMetrics) != 2 {
		t.Fatalf("unexpected sleep metric catalog sizes: all=%d traditional=%d coarse=%d", len(sleepMetricNames), len(sleepTraditionalMetrics), len(sleepCoarseMetrics))
	}
}

func TestEmbeddedAggregateTemplatesResolveAllStaticFragments(t *testing.T) {
	queries := []string{
		aggregateSQL("live_hourly_avg"),
		aggregateSQL("live_hourly_sum"),
		aggregateSQLWith("live_hourly_sleep", map[string]string{"SLEEP_DEDUP": sleepDedupClause("sleep_total")}),
		aggregateSQLWith("live_daily", map[string]string{
			"SLEEP_PICK_SOURCE":         sleepCrossValidationPickSourceExpr("sleep_total_per_source", "sum_val"),
			"SLEEP_TRADITIONAL_METRICS": sqlStringList(sleepTraditionalMetrics),
			"SLEEP_COARSE_METRICS":      sqlStringList(sleepCoarseMetrics),
			"SUM_WATCH_CONDITION":       sourcePriorityCondition("source", sumSourcePriority[0]),
			"SUM_IPHONE_CONDITION":      sourcePriorityCondition("source", sumSourcePriority[1]),
		}),
		aggregateSQLWith("legacy_hourly_avg", map[string]string{"SLEEP_DEDUP": "", "FROM_CLAUSE": ""}),
		aggregateSQLWith("legacy_hourly_sum", map[string]string{"SLEEP_DEDUP": "", "FROM_CLAUSE": ""}),
		aggregateSQLWith("legacy_daily_avg", map[string]string{"FROM_CLAUSE": ""}),
		aggregateSQLWith("legacy_daily_sum", map[string]string{"FROM_CLAUSE": "", "SUM_SOURCE_RANK": legacySumSourceRankExpr()}),
		aggregateSQLWith("legacy_daily_upsert", map[string]string{"COLUMN": "steps"}),
		aggregateSQLWith("legacy_daily_sleep", map[string]string{
			"FROM_CLAUSE":               "",
			"SLEEP_METRICS":             sqlStringList(sleepMetricNames),
			"SLEEP_TRADITIONAL_METRICS": sqlStringList(sleepTraditionalMetrics),
			"SLEEP_COARSE_METRICS":      sqlStringList(sleepCoarseMetrics),
			"SLEEP_PRIORITY_CASE":       sleepSourcePriorityCaseExpr("source"),
			"SLEEP_MIN_HOURS":           "1",
			"SLEEP_DIVERGENCE":          "1.4",
		}),
	}
	for i, query := range queries {
		if strings.Contains(query, "{{") {
			t.Errorf("query %d contains an unresolved static SQL marker", i)
		}
	}
}

func TestAggregateSQLTemplateRejectsMarkerDrift(t *testing.T) {
	assertPanics := func(name string, replacements map[string]string) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("aggregateSQLWith(%q) did not reject marker drift", name)
			}
		}()
		aggregateSQLWith(name, replacements)
	}
	assertPanics("live_hourly_avg", map[string]string{"UNUSED": "value"})
	assertPanics("legacy_daily_upsert", nil)
}

func TestBuildDailyMetricColRejectsUnlistedColumnBeforeDatabaseAccess(t *testing.T) {
	var db DB
	if err := db.buildDailyMetricCol("unsafe_column", "step_count", true); err == nil {
		t.Fatal("buildDailyMetricCol accepted a column outside the static allowlist")
	}
}

func TestLegacySumRankPreservesSourceOrder(t *testing.T) {
	rank := legacySumSourceRankExpr()
	watch := strings.Index(rank, "Ultra")
	phone := strings.Index(rank, "iPhone")
	if watch < 0 || phone < 0 || watch > phone {
		t.Fatalf("legacy SUM source ranking lost Watch-before-iPhone order: %s", rank)
	}
}
