package storage

import (
	"slices"
	"strings"
	"testing"
)

// Pure-function guards that catch the easy ways to lose the
// sleep_unspecified plumbing in a future refactor. Anything that needs a
// live DB (atomicity gate, COALESCE preserve-prior, historical migration)
// is verified end-to-end on staging — see SLEEP_UNSPECIFIED_ROLLOUT.md
// §Server tests.

func TestSleepUnspecified_IsSumMetric(t *testing.T) {
	if !SumMetrics["sleep_unspecified"] {
		t.Fatalf("sleep_unspecified must be in SumMetrics — aggregator skips it otherwise")
	}
}

func TestSleepUnspecified_IsSleepMetric(t *testing.T) {
	if !isSleepMetric("sleep_unspecified") {
		t.Fatalf("isSleepMetric must classify sleep_unspecified as a sleep metric")
	}
}

func TestSleepUnspecified_HasDedupClause(t *testing.T) {
	clause := sleepDedupClause("sleep_unspecified")
	if clause == "" {
		t.Fatalf("sleepDedupClause must emit the midnight-summary filter for sleep_unspecified")
	}
	if !strings.Contains(clause, "00:00:00") {
		t.Fatalf("dedup clause must filter the midnight-summary record, got: %s", clause)
	}
}

func TestSleepUnspecified_InSumMetricSlice(t *testing.T) {
	if !slices.Contains(sumMetricSlice(), "sleep_unspecified") {
		t.Fatalf("sumMetricSlice must include sleep_unspecified — UpsertRecentCache passes this slice to filter SUM metrics")
	}
}
