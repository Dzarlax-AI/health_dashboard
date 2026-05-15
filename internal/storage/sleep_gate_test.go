package storage

import "testing"

// Sleep atomicity gate regression tests (issue #79).
//
// The four scenarios spec'd in the issue:
//
//  1. Mixed-source night where picked source has 5 stages → write all.
//  2. Mixed-source night where picked source has total + unspecified
//     only → write the block (coarse-only branch passes).
//  3. Mixed-source night where picked source has total only → preserve
//     prior row (gate fails by design, issue #77).
//  4. Single-source night → always pass (defensive: avoid erasing an
//     Apple Watch night that lost its sleep_awake row to the qty>0
//     filter in buildHourlyMetric).
//
// These tests exercise the Go twin of the SQL gate
// (EvaluateSleepPickedComplete in sleep_gate.go). The SQL is the real
// path in production; the Go twin is here so unit tests can catch
// the gate logic drifting without a live Postgres container. Any
// change to the SQL gate MUST mirror in the Go function or these
// tests catch it.

func TestSleepGate_SingleSource_AnyMetrics_Passes(t *testing.T) {
	// Even with only one metric (sleep_total alone), single-source
	// nights trust the source as-is. Common case: Apple Watch night
	// where sleep_awake = 0 gets filtered upstream, leaving 4 of 5
	// stages — gate must not erase this.
	cases := [][]string{
		{"sleep_total"},
		{"sleep_total", "sleep_deep"},
		{"sleep_total", "sleep_deep", "sleep_rem", "sleep_core", "sleep_awake"},
		{"sleep_total", "sleep_unspecified"},
		{}, // even no metrics at all — n_sources <=1 shortcut wins
	}
	for _, metrics := range cases {
		if !EvaluateSleepPickedComplete(1, metrics) {
			t.Errorf("single source must pass, but failed with metrics=%v", metrics)
		}
	}
}

func TestSleepGate_MultiSource_FullStages_Passes(t *testing.T) {
	// Stage-tracking device (Apple Watch with sleep classification on)
	// — emits all 5 traditional stages → gate passes, daily_scores
	// gets the picked source's block written atomically.
	full := []string{"sleep_total", "sleep_deep", "sleep_rem", "sleep_core", "sleep_awake"}
	if !EvaluateSleepPickedComplete(2, full) {
		t.Error("multi-source + full 5 stages must pass")
	}
	// Order shouldn't matter.
	scrambled := []string{"sleep_awake", "sleep_total", "sleep_rem", "sleep_deep", "sleep_core"}
	if !EvaluateSleepPickedComplete(2, scrambled) {
		t.Error("metric order must not affect the gate")
	}
}

func TestSleepGate_MultiSource_CoarseOnly_Passes(t *testing.T) {
	// Coarse-only device (RingConn, iPhone Sleep Schedule, older
	// Apple Watch) — emits total + unspecified. Must pass via the
	// second OR branch added in v2.3, otherwise the multi-source
	// night where MIN-pick lands on this source would silently
	// preserve the prior row and visually skip the night.
	if !EvaluateSleepPickedComplete(2, []string{"sleep_total", "sleep_unspecified"}) {
		t.Error("multi-source + total + unspecified must pass (coarse-only branch)")
	}
	// Awake from a coarse device is also fine — the gate counts the
	// two anchor metrics, extras don't disqualify.
	if !EvaluateSleepPickedComplete(2, []string{"sleep_total", "sleep_unspecified", "sleep_awake"}) {
		t.Error("multi-source + total + unspecified + awake must also pass")
	}
}

func TestSleepGate_MultiSource_TotalOnly_Fails(t *testing.T) {
	// Known limitation (issue #77): a multi-source night where the
	// picked source emits ONLY sleep_total fails by design. This
	// prevents a malformed importer from wiping a real staged night
	// by writing a single sleep_total row.
	if EvaluateSleepPickedComplete(2, []string{"sleep_total"}) {
		t.Error("multi-source + sleep_total only must fail (issue #77 design)")
	}
}

func TestSleepGate_MultiSource_PartialStages_Fails(t *testing.T) {
	// Three or four of the traditional stages but not all five → fails.
	// Stage-tracking device should always emit the full set; missing
	// one suggests data corruption or mid-pipeline filter. Don't write
	// a half-block over a prior good one.
	cases := [][]string{
		{"sleep_total", "sleep_deep", "sleep_rem"},
		{"sleep_total", "sleep_deep", "sleep_rem", "sleep_core"}, // 4 of 5
		{"sleep_total", "sleep_deep", "sleep_rem", "sleep_awake"},
	}
	for _, m := range cases {
		if EvaluateSleepPickedComplete(2, m) {
			t.Errorf("multi-source + partial stages must fail, but passed with %v", m)
		}
	}
}

func TestSleepGate_MultiSource_StagesPlusUnspecified_Passes(t *testing.T) {
	// Edge case: picked source emits both the 5 traditional stages AND
	// sleep_unspecified (shouldn't happen with native data but defensive
	// against a future quirky source). Either branch alone is enough —
	// the gate should pass.
	mixed := []string{"sleep_total", "sleep_deep", "sleep_rem", "sleep_core", "sleep_awake", "sleep_unspecified"}
	if !EvaluateSleepPickedComplete(2, mixed) {
		t.Error("multi-source + full stages + unspecified must pass")
	}
}

func TestSleepGate_Empty_Fails(t *testing.T) {
	// Multi-source but picked source contributed no metrics? Edge case
	// (race between source-pick query and per_source GROUP BY). Fail
	// the gate, preserve prior.
	if EvaluateSleepPickedComplete(2, nil) {
		t.Error("multi-source + zero metrics must fail")
	}
	if EvaluateSleepPickedComplete(2, []string{}) {
		t.Error("multi-source + empty metrics must fail")
	}
}

// TestSleepGate_DuplicateMetrics_NoDoubleCount: a malformed input where
// the same metric_name appears twice in the picked-source list (could
// happen if a future caller forgets DISTINCT) must NOT inflate the
// completeness count past the gate. metricSet deduplicates via the map,
// so the gate remains decision-equivalent to the SQL COUNT(DISTINCT).
func TestSleepGate_DuplicateMetrics_NoDoubleCount(t *testing.T) {
	dup := []string{"sleep_total", "sleep_total", "sleep_total", "sleep_total", "sleep_total"}
	if EvaluateSleepPickedComplete(2, dup) {
		t.Error("five duplicates of sleep_total must not be treated as 5 stages")
	}
}
