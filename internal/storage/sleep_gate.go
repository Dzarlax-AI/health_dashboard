package storage

// EvaluateSleepPickedComplete is the Go-side mirror of the SQL
// `sleep_picked_complete` CTE used by upsertDailyForDate and
// buildDailySleepBlock. The SQL is the real gate that decides whether
// daily_scores.sleep_* gets overwritten or preserved-via-COALESCE; this
// function exists so the same decision is exercisable from unit tests
// (issue #79) without a live Postgres container.
//
// Both surfaces must stay in lockstep:
//   - SQL gate: see the `sleep_picked_complete AS (...)` CTE in
//     internal/storage/aggregates.go.
//   - Go gate:  this function. Same three branches.
//
// Inputs:
//   - nSources: number of distinct sources that contributed sleep_total
//     for the night (a.k.a. COUNT(DISTINCT source) FROM
//     sleep_total_per_source).
//   - pickedMetrics: the set of sleep_* metric names emitted by the
//     picked source for the night (a.k.a. metric_name values in
//     per_source WHERE source = pickedSource).
//
// Returns true when the gate would write the picked source's stages
// into daily_scores; false when the gate would keep the prior row's
// values via COALESCE preserve.
//
// Three branches (any one passes the gate):
//
//  1. n_sources <= 1 — single source, trust as-is. Apple Watch nights
//     with sleep_awake = 0 lose the awake row in hourly_metrics (qty>0
//     filter); requiring 5/5 metrics here would erase a perfectly good
//     four-stage night.
//
//  2. multi-source + picked has all 5 traditional stages (total + deep
//     + rem + core + awake) — stage-tracking device, write the block.
//
//  3. multi-source + picked has total + unspecified (coarse-only
//     device — RingConn / iPhone Sleep Schedule / older Apple Watch).
//     Without this branch, a multi-source night where cross-validation
//     picks the coarse source would fall through to NULL writes and
//     the prior block would survive untouched.
//
// Known limitation (issue #77): a multi-source night where the picked
// source emits ONLY sleep_total (no stages, no unspecified) fails —
// returns false. Deliberate; accepting a single-metric pick would let
// a malformed third-party importer wipe a real staged night.
func EvaluateSleepPickedComplete(nSources int, pickedMetrics []string) bool {
	if nSources <= 1 {
		return true
	}
	has := metricSet(pickedMetrics)

	if has["sleep_total"] && has["sleep_deep"] && has["sleep_rem"] && has["sleep_core"] && has["sleep_awake"] {
		return true
	}
	if has["sleep_total"] && has["sleep_unspecified"] {
		return true
	}
	return false
}

func metricSet(metrics []string) map[string]bool {
	out := make(map[string]bool, len(metrics))
	for _, m := range metrics {
		out[m] = true
	}
	return out
}
