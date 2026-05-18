package storage

import (
	"os"
	"path/filepath"
	"regexp"
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
		t.Fatalf("dedup clause must reference the midnight-summary record, got: %s", clause)
	}
}

// TestSleepDedupClause_PrefersMidnightSummary pins the policy invariant:
// when both midnight summary and per-segment fragments exist for the same
// (metric, source, night), the clause keeps the summary and drops the
// fragments. The earlier policy inverted this and produced inflated stage
// sums on Apple Watch nights (sleep_core 6.3h vs canonical 5.5h). See
// the Methodology Status section in SCORING.md and Todoist 6ggC4VqJ3pgmF4h7
// for the incident write-up.
//
// Pure string assertion — the live SQL behaviour is exercised end-to-end
// by the daily_scores backfill sanity check, since the dedup runs inside
// a Postgres EXISTS subquery.
func TestSleepDedupClause_PrefersMidnightSummary(t *testing.T) {
	for _, metric := range []string{"sleep_total", "sleep_deep", "sleep_rem", "sleep_core", "sleep_awake", "sleep_unspecified"} {
		clause := sleepDedupClause(metric)
		if clause == "" {
			t.Fatalf("%s: clause unexpectedly empty", metric)
		}
		// The outer filter must check that the row is NOT a midnight
		// summary, and the inner EXISTS must require a midnight summary
		// row to exist. That combination drops fragments when a summary
		// is present.
		if !strings.Contains(clause, `SUBSTRING(date, 12, 8) != '00:00:00'`) {
			t.Errorf("%s: outer predicate must target non-midnight rows; got: %s", metric, clause)
		}
		if !strings.Contains(clause, `SUBSTRING(p2.date, 12, 8) = '00:00:00'`) {
			t.Errorf("%s: inner EXISTS must require a midnight summary row; got: %s", metric, clause)
		}
	}
}

// TestSleepDedup_NoInlineCopies catches stale inline copies of the
// old "drop midnight when fragments exist" SQL rule. The canonical
// version lives in sleepDedupClause; any other occurrence in
// storage/*.go means a write path will drift on the next backfill,
// reintroducing the inflation this fix removed.
//
// Caught by Codex review on PR #120 — buildHourlyMetric was still
// running the old rule on `make backfill-force`, so the planned
// rebuild would have recreated the inflated sleep_core values.
//
// The fingerprint is the inner EXISTS predicate of the OLD rule:
// "p2.date" with "!= '00:00:00'" (or `<>`). The NEW rule's inner
// EXISTS uses `= '00:00:00'`, so the !=/<> form is the smoking gun.
func TestSleepDedup_NoInlineCopies(t *testing.T) {
	// Match either "!= '00:00:00'" or "<> '00:00:00'" with arbitrary
	// whitespace around the operator. Anchored to p2.date so we don't
	// flag the outer predicate of the canonical clause (which targets
	// the row being filtered, not the correlated row).
	re := regexp.MustCompile(`p2\.date[^,)]*?(!=|<>)\s*'00:00:00'`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read storage dir: %v", err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(".", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if loc := re.FindIndex(data); loc != nil {
			line := strings.Count(string(data[:loc[0]]), "\n") + 1
			t.Errorf("%s:%d looks like an inline copy of the OLD sleep dedup rule (p2.date != '00:00:00'). Use sleepDedupClause() instead — see TestSleepDedupClause_PrefersMidnightSummary for the canonical shape.", path, line)
		}
	}
}

func TestSleepUnspecified_InSumMetricSlice(t *testing.T) {
	if !slices.Contains(sumMetricSlice(), "sleep_unspecified") {
		t.Fatalf("sumMetricSlice must include sleep_unspecified — UpsertRecentCache passes this slice to filter SUM metrics")
	}
}
