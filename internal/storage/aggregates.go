package storage

import (
	"fmt"
	"log"
	"strings"
	"sync"
)

// aggFuncFor returns the aggregation function name for a metric.
// SUM metrics accumulate within a period; all others are averaged.
func aggFuncFor(metric string) string {
	if SumMetrics[metric] {
		return "SUM"
	}
	return "AVG"
}

// combineFuncFor returns the SQL aggregate to combine per-source pre-computed
// values when merging sources at query time.
//   - AVG metrics: AVG across sources
//   - SUM metrics: smart dedup (see sumCombineExpr)
func combineFuncFor(metric string) string {
	if SumMetrics[metric] {
		return "MAX" // only used in fallback paths; prefer sumCombineExpr
	}
	return "AVG"
}

// sumCombineExpr returns `MAX(valCol)` — picks the source with the highest
// total for SUM metrics. Used for per-hour dedup in raw metric_points queries
// where sources overlap within a single timeslot.
func sumCombineExpr(valCol string) string {
	return "MAX(" + valCol + ")"
}

// sleepCrossValidationPickExpr returns a SQL `CASE … END` expression that
// picks the best source's per-day total for sleep metrics from a subquery
// that exposes a `source` column and a per-source value column named
// `valCol` (typically `source_total` or `source_sum`).
//
// Priority: Apple Watch > RingConn > anything else.
//
// Cross-validation: when MULTIPLE sources both registered a non-trivial
// sleep total (MIN > 1h) AND they disagree by >40%, the higher value is
// likely an outlier — take MIN to be conservative. This catches RingConn
// occasionally reporting a wildly inflated 14h-night while the watch
// shows 7h, but excludes the much more common "RingConn wrote a 0.x-hour
// daily-summary stub on a watch-only night" case where MIN is just noise
// and the priority-COALESCE branch should pick Watch instead.
//
// Used in four read paths: preferredSleepSourceSQL constant (uses table
// `source_totals`), metricDataDayFromHourly, metricDataRaw, and
// briefing.go's fetch helper. Keep them in sync via this helper rather
// than hand-edited copies — five separate fixes shipped over PRs #8/#9/#10
// before all paths were guarded.
//
// Daily-write paths use the source-twin sleepCrossValidationPickSourceExpr
// (single-day, in upsertDailyForDate) and an inlined multi-day variant
// in buildDailySleepBlock; both must keep the 1.0h floor and 1.4×
// divergence threshold in lockstep with this helper.
func sleepCrossValidationPickExpr(valCol string) string {
	return `CASE
		WHEN COUNT(*) > 1 AND MIN(` + valCol + `) > 1.0
		 AND MAX(` + valCol + `) > MIN(` + valCol + `) * 1.4
		THEN MIN(` + valCol + `)
		ELSE COALESCE(
		    MAX(CASE WHEN source LIKE '%Ultra%' OR source LIKE '%Apple Watch%' THEN ` + valCol + ` END),
		    MAX(CASE WHEN source LIKE '%RingConn%' THEN ` + valCol + ` END),
		    MAX(` + valCol + `)
		)
	END`
}

// sleepCrossValidationPickSourceExpr is the source-name twin of
// sleepCrossValidationPickExpr: same priority and cross-validation rules,
// but returns the SOURCE that wins rather than its value. Used when
// downstream queries need to pull multiple stages from a single device
// (e.g. upsertDailyForDate picks the source by sleep_total totals, then
// reads sleep_deep / sleep_rem / sleep_core / sleep_awake from that same
// device so phase ratios stay physically consistent).
//
// `table` must be a pre-filtered relation (CTE / subquery) exposing a
// `source` column and the value column named `valCol`. Caller is
// responsible for filtering to one metric (typically `sleep_total`).
//
// Keep the thresholds (MIN > 1.0, 1.4× divergence) in lockstep with
// sleepCrossValidationPickExpr — that is the whole point of having a
// shared helper instead of ad-hoc inline CASEs.
func sleepCrossValidationPickSourceExpr(table, valCol string) string {
	// Tiebreak by `source ASC` so two rows with identical totals always
	// resolve to the same pick across reruns. Without this, Postgres can
	// return either row when sums tie, and the picked source flips
	// between backfills — causing daily_scores.sleep_* to drift even
	// with no new data.
	return `(
		CASE
			WHEN (SELECT COUNT(*) FROM ` + table + `) > 1
			 AND (SELECT MIN(` + valCol + `) FROM ` + table + `) > 1.0
			 AND (SELECT MAX(` + valCol + `) FROM ` + table + `) >
			     (SELECT MIN(` + valCol + `) FROM ` + table + `) * 1.4
			THEN (SELECT source FROM ` + table + ` ORDER BY ` + valCol + ` ASC, source ASC LIMIT 1)
			ELSE COALESCE(
				(SELECT source FROM ` + table + `
				  WHERE source LIKE '%Ultra%' OR source LIKE '%Apple Watch%'
				  ORDER BY ` + valCol + ` DESC, source ASC LIMIT 1),
				(SELECT source FROM ` + table + `
				  WHERE source LIKE '%RingConn%'
				  ORDER BY ` + valCol + ` DESC, source ASC LIMIT 1),
				(SELECT source FROM ` + table + ` ORDER BY ` + valCol + ` DESC, source ASC LIMIT 1)
			)
		END
	)`
}

// preferredSourceSQL returns a SQL snippet that picks the best source's daily
// total from a subquery with (source, source_total) columns.
// Priority: Apple Watch ("Ultra") > iPhone > other (e.g. RingConn).
// Falls back to MAX(source_total) if no Apple device is present.
const preferredSourceSQL = `
	SELECT COALESCE(
		(SELECT source_total FROM source_totals
		 WHERE source LIKE '%Ultra%' OR source LIKE '%Apple Watch%'
		 ORDER BY source_total DESC LIMIT 1),
		(SELECT source_total FROM source_totals
		 WHERE source LIKE '%iPhone%'
		 ORDER BY source_total DESC LIMIT 1),
		(SELECT MAX(source_total) FROM source_totals)
	)`

// preferredSleepSourceSQL picks the best source for sleep metrics from a
// `source_totals` CTE in the calling query (columns: source, source_total).
// Priority: Apple Watch > RingConn > other. Cross-validation: when multiple
// sources exist AND each registered a non-trivial total (MIN > 1h), AND MAX/MIN
// differ by >40%, the higher value is likely an outlier — take MIN instead.
//
// Implementation: wrap sleepCrossValidationPickExpr in a scalar SELECT against
// source_totals so the helper's aggregate form (no GROUP BY → single group)
// produces a scalar value. This keeps the thresholds (1.0h floor, 1.4×) and
// source priority in the helper, not duplicated here. Previously this was a
// hand-rolled CASE tree; consolidated per CodeRabbit on PR #26.
//
// `var` (not `const`) because the value depends on a function call. Caller
// inserts this inside a `(WITH source_totals AS (...) %s)` scalar subquery,
// so it must be a bare `SELECT … FROM source_totals` (no outer parens).
var preferredSleepSourceSQL = `SELECT ` + sleepCrossValidationPickExpr("source_total") + ` FROM source_totals`

func preferredSourceForMetric(metric string) string {
	if strings.HasPrefix(metric, "sleep_") {
		return preferredSleepSourceSQL
	}
	return preferredSourceSQL
}

// SumMetrics is the canonical set of metrics that should be SUMmed within a bucket.
// Exported so the MCP server can use the same classification without duplication.
var SumMetrics = map[string]bool{
	"step_count": true, "active_energy": true, "basal_energy_burned": true,
	"apple_exercise_time": true, "apple_stand_time": true,
	"flights_climbed": true, "walking_running_distance": true,
	"time_in_daylight": true, "apple_stand_hour": true,
	// sleep phases are SUM'd per source, then MAX'd across sources
	"sleep_total": true, "sleep_deep": true, "sleep_rem": true,
	"sleep_core": true, "sleep_awake": true,
	// sleep_unspecified — coarse asleep total from sources without a
	// deep/REM/core breakdown (RingConn, iPhone-only, older Apple Watch).
	// SUM like the other stages; mutually exclusive with deep/rem/core
	// per source. Once the v2.3 iOS client ships, RingConn-only nights
	// land here instead of inflating sleep_core.
	"sleep_unspecified": true,
	// New-format split written by health-sync iOS — same SUM semantics
	// as sleep_total. Treated as plain time-series; not yet cached in
	// daily_scores (read directly from metric_points by briefing.go).
	"night_sleep_total": true, "nap_total": true,
}

// sleepDedupClause returns a SQL WHERE clause that excludes per-segment sleep
// fragments when a midnight summary (00:00:00) exists for the same day+source.
// Returns empty string for non-sleep metrics.
//
// Policy: prefer the device's midnight summary because it represents the
// source's own reconciled answer for "the night that ended at 00:00".
// Per-segment fragments are the underlying detection events and tend to
// double-count: pairs at 1-second offset (e.g. sleep_deep 02:48:17 +
// 02:48:18), daytime "core sleep" segments while the user was still,
// overlapping summaries when a session is re-emitted. Falling back to
// fragments only when no summary exists preserves the Round 1 fix
// (PR #3, days where the summary went missing).
//
// Invariant this enforces: for Apple Watch (which emits both formats),
// sleep_total ≈ sleep_deep + sleep_rem + sleep_core per night per source.
// sleep_awake is separate (time awake within the sleep period), not part
// of sleep_total.
func sleepDedupClause(metric string) string {
	if !isSleepMetric(metric) {
		return ""
	}
	return `AND NOT (
		SUBSTRING(date, 12, 8) != '00:00:00'
		AND EXISTS (
			SELECT 1 FROM metric_points p2
			WHERE p2.metric_name = metric_points.metric_name
			  AND SUBSTRING(p2.date, 1, 10) = SUBSTRING(metric_points.date, 1, 10)
			  AND p2.source = metric_points.source
			  AND SUBSTRING(p2.date, 12, 8) = '00:00:00'
			  AND p2.qty > 0
			  AND p2.quality = 'ok'
		)
	)`
}

func (s *DB) listMetricNames() ([]string, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT DISTINCT metric_name FROM metric_points ORDER BY metric_name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var m string
		rows.Scan(&m)
		out = append(out, m)
	}
	return out, rows.Err()
}

// UpsertRecentCache rebuilds hourly_metrics and daily_scores for the given
// dates directly from metric_points, then optionally recomputes readiness for
// affected dates. Called inline after POST /health so the cache is always
// fresh — no "hole" between invalidation and backfill.
//
// Per date this issues 4 SQL statements (non-sleep AVG, non-sleep SUM, sleep
// dedup, daily roll-up). When `recomputeReadiness` is true an extra
// readiness recomputation pass runs once for the whole date range — the
// caller passes false when only non-score metrics changed (e.g. step_count
// alone) to skip it.
func (s *DB) UpsertRecentCache(dates []string, recomputeReadiness bool) {
	if len(dates) == 0 {
		return
	}
	// Tenant TZ is read once per UpsertRecentCache pass and reused for
	// every date — REPORT_TZ doesn't change mid-process, and
	// `time.LoadLocation` allocates ~5 KB per call which adds up over a
	// 48-hour incremental window. Fallback to UTC inside
	// reportTZLocation matches energy_compute.go convention.
	loc := reportTZLocation()
	s.cacheMu.Lock()
	for _, date := range dates {
		s.upsertHourlyAvgForDate(date)
		s.upsertHourlySumForDate(date)
		s.upsertHourlySleepForDate(date)
		s.upsertDailyForDate(date)
		// Must run AFTER upsertDailyForDate — the latter creates the
		// daily_scores row that the baseline UPDATE targets. Cheap
		// enough to run inside the same critical section (one
		// percentile query + one UPDATE per date).
		s.upsertBaselineHROvernightForDate(date, loc)
		// v2.2 sustained_hr_load — depends on baseline_hr_overnight
		// being current AND on the personal HR baseline being
		// readable, so runs last in the per-date chain.
		_, _ = s.upsertSustainedHRLoadForDate(date, loc)
	}
	s.cacheMu.Unlock()

	if !recomputeReadiness {
		return
	}
	earliest := dates[0]
	for _, d := range dates[1:] {
		if d < earliest {
			earliest = d
		}
	}
	s.RecomputeReadinessSince(earliest)
}

// upsertHourlyAvgForDate rebuilds hourly_metrics for ALL non-sleep AVG metrics
// on `date` in a single SQL statement.
func (s *DB) upsertHourlyAvgForDate(date string) {
	ctx, cancel := longCtx()
	defer cancel()
	const q = `
		INSERT INTO hourly_metrics (metric_name, hour, source, avg_val, min_val, max_val)
		SELECT metric_name,
		       SUBSTRING(date, 1, 13) || ':00' AS hour,
		       source,
		       AVG(qty), MIN(qty), MAX(qty)
		FROM metric_points
		WHERE SUBSTRING(date,1,10) = $1
		  AND qty > 0
		  AND quality = 'ok'
		  AND metric_name NOT LIKE 'sleep\_%' ESCAPE '\'
		  AND metric_name <> ALL($2::text[])
		GROUP BY metric_name, SUBSTRING(date, 1, 13) || ':00', source
		ON CONFLICT (metric_name, hour, source) DO UPDATE SET
			avg_val=EXCLUDED.avg_val, min_val=EXCLUDED.min_val, max_val=EXCLUDED.max_val`
	if _, err := s.pool.Exec(ctx, q, date, sumMetricSlice()); err != nil {
		log.Printf("upsert hourly avg %s: %v", date, err)
	}
}

// upsertHourlySumForDate rebuilds hourly_metrics for non-sleep SUM metrics
// (steps, active_energy, etc.) — one SQL for all of them.
func (s *DB) upsertHourlySumForDate(date string) {
	ctx, cancel := longCtx()
	defer cancel()
	const q = `
		INSERT INTO hourly_metrics (metric_name, hour, source, avg_val, min_val, max_val)
		SELECT metric_name, hour, source,
		       SUM(minute_max) AS sum_val, MIN(minute_min) AS min_val, MAX(minute_max) AS max_val
		FROM (
			SELECT metric_name, source,
			       SUBSTRING(date, 1, 13) || ':00' AS hour,
			       SUBSTRING(date, 1, 16) AS minute,
			       MAX(qty) AS minute_max, MIN(qty) AS minute_min
			FROM metric_points
			WHERE SUBSTRING(date,1,10) = $1
			  AND qty > 0
			  AND quality = 'ok'
			  AND metric_name = ANY($2::text[])
			  AND metric_name NOT LIKE 'sleep\_%' ESCAPE '\'
			GROUP BY metric_name, source,
			         SUBSTRING(date, 1, 13) || ':00',
			         SUBSTRING(date, 1, 16)
		) sub
		GROUP BY metric_name, hour, source
		ON CONFLICT (metric_name, hour, source) DO UPDATE SET
			avg_val=EXCLUDED.avg_val, min_val=EXCLUDED.min_val, max_val=EXCLUDED.max_val`
	if _, err := s.pool.Exec(ctx, q, date, sumMetricSlice()); err != nil {
		log.Printf("upsert hourly sum %s: %v", date, err)
	}
}

// upsertHourlySleepForDate handles the 5 sleep_* metrics with the dedup clause.
// Delegates the dedup rule to sleepDedupClause — single source of truth
// shared with the live ingest path (per-metric callers) and the force-
// rebuild path (buildHourlyMetric). The clause body is metric-independent
// for sleep metrics; any sleep metric name passed to sleepDedupClause
// returns the same SQL fragment.
//
// Note: the query drops table aliases because sleepDedupClause's
// correlated EXISTS subquery references the bare `metric_points` columns
// (e.g. `metric_points.metric_name`).
//
// The DELETE before INSERT is load-bearing: the prior dedup policy wrote
// per-hour fragment rows (one per sleep segment), and the new policy
// writes a single 00:00 summary row. ON CONFLICT only updates the row
// keyed by (metric_name, hour, source), so without the DELETE the old
// fragment rows for this date would survive and upsertDailyForDate
// would sum them with the new summary — double counting the night.
func (s *DB) upsertHourlySleepForDate(date string) {
	ctx, cancel := longCtx()
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		log.Printf("upsert hourly sleep %s: begin tx: %v", date, err)
		return
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		DELETE FROM hourly_metrics
		WHERE SUBSTRING(hour,1,10) = $1
		  AND metric_name LIKE 'sleep\_%' ESCAPE '\'`, date); err != nil {
		log.Printf("upsert hourly sleep %s: delete stale: %v", date, err)
		return
	}

	q := `
		INSERT INTO hourly_metrics (metric_name, hour, source, avg_val, min_val, max_val)
		SELECT metric_name, hour, source,
		       SUM(minute_max), MIN(minute_min), MAX(minute_max)
		FROM (
			SELECT metric_name, source,
			       SUBSTRING(date, 1, 13) || ':00' AS hour,
			       SUBSTRING(date, 1, 16) AS minute,
			       MAX(qty) AS minute_max, MIN(qty) AS minute_min
			FROM metric_points
			WHERE SUBSTRING(date,1,10) = $1
			  AND qty > 0
			  AND quality = 'ok'
			  AND metric_name LIKE 'sleep\_%' ESCAPE '\'
			  ` + sleepDedupClause("sleep_total") + `
			GROUP BY metric_name, source,
			         SUBSTRING(date, 1, 13) || ':00',
			         SUBSTRING(date, 1, 16)
		) sub
		GROUP BY metric_name, hour, source
		ON CONFLICT (metric_name, hour, source) DO UPDATE SET
			avg_val=EXCLUDED.avg_val, min_val=EXCLUDED.min_val, max_val=EXCLUDED.max_val`
	if _, err := tx.Exec(ctx, q, date); err != nil {
		log.Printf("upsert hourly sleep %s: insert: %v", date, err)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("upsert hourly sleep %s: commit: %v", date, err)
	}
}

// sumMetricSlice returns the SumMetrics map keys as a slice (for $::text[] params).
func sumMetricSlice() []string {
	out := make([]string, 0, len(SumMetrics))
	for m := range SumMetrics {
		out = append(out, m)
	}
	return out
}

// upsertDailyForDate rebuilds daily_scores metric columns for one date in
// ONE statement: per-metric (source, total) totals are computed once via CTE,
// preferred-source / sleep-cross-validation logic is applied per metric, and
// the result lands in daily_scores via INSERT ... ON CONFLICT DO UPDATE.
//
// Replaces a previous loop that issued 13 SELECTs + 13 UPSERTs in a tx.
func (s *DB) upsertDailyForDate(date string) {
	ctx, cancel := longCtx()
	defer cancel()

	// Per-metric resolution rule:
	//   AVG metrics  → AVG(avg_val) across sources
	//   SUM (sleep)  → ONE source picked once for the night via sleep_total
	//                  cross-validation; all five sleep_* stages then come
	//                  from that same source (see sleep_picked CTE below).
	//   SUM (other)  → preferred source pick (Apple Watch > iPhone > MAX)
	//
	// Why pick the source once for sleep: resolving each phase independently
	// can mix Apple Watch's REM with RingConn's sleep_total (when cross-
	// validation picks MIN), producing physically impossible ratios such as
	// REM/Total > 100%. Latent today (single source — Apple Watch only) but
	// would resurface immediately if RingConn is re-enabled.
	q := `
WITH per_source AS (
    SELECT metric_name, source,
           AVG(avg_val) AS avg_val,
           SUM(avg_val) AS sum_val
    FROM hourly_metrics
    WHERE SUBSTRING(hour,1,10) = $1
    GROUP BY metric_name, source
),
sleep_total_per_source AS (
    SELECT source, sum_val FROM per_source WHERE metric_name = 'sleep_total'
),
-- Pick ONE source for tonight from sleep_total totals; all five sleep_*
-- stages will be filtered to this source so phase ratios stay physically
-- consistent. Helper keeps thresholds in lockstep with the value-twin.
sleep_picked AS (
    SELECT ` + sleepCrossValidationPickSourceExpr("sleep_total_per_source", "sum_val") + ` AS src
),
-- Atomicity gate: prevent mixed-source writes when MULTIPLE sources
-- contribute sleep_total for the night. With a single source there is
-- no mixing risk, and a strict 5-stage requirement would erase a real
-- night just because one stage happened to be 0 (e.g. a HAE-fed Apple
-- Watch night with sleep_awake = 0 — buildHourlyMetric filters qty>0
-- so the awake row never reaches hourly_metrics, completeness fails,
-- and all five sleep_* columns get NULL instead of the four real ones).
-- Therefore: when n_sources <= 1, trust the only source as-is.
-- When n_sources > 1, require ALL five stages from picked source so we
-- don't COALESCE-preserve a prior row's stage from a different device.
sleep_picked_complete AS (
    -- Conditional gate (option B per SLEEP_UNSPECIFIED_ROLLOUT.md):
    --   single source     → trust as-is
    --   multi-source + picked has all 5 traditional stages → complete (stage-tracking device)
    --   multi-source + picked has sleep_total + sleep_unspecified → complete (coarse-only device)
    -- Without the second clause, multi-source nights where MIN-pick lands
    -- on a RingConn-only source (2 metrics) would fall through to NULL
    -- writes and the prior block would survive untouched.
    --
    -- KNOWN LIMITATION (Issue #77): a multi-source night where the picked
    -- source emits ONLY sleep_total (no stages, no sleep_unspecified) does
    -- not match either branch — the gate fails, the prior daily_scores row
    -- is preserved, and that source's contribution is silently dropped for
    -- this night. Deliberate choice rather than oversight: accepting a
    -- single-metric pick would let a malformed third-party importer wipe
    -- a real staged night by writing only sleep_total. The v2.3 iOS
    -- client always pairs sleep_total with sleep_unspecified for coarse
    -- sources, and the HK XML importer in internal/applehealth/parse.go
    -- maps both AsleepUnspecified and bare Asleep to sleep_unspecified,
    -- so natively-imported data cannot hit this corner.
    --
    -- Regression coverage: internal/storage/sleep_gate.go mirrors this
    -- logic as a pure Go function (EvaluateSleepPickedComplete) so unit
    -- tests can exercise all four scenarios without a live Postgres.
    -- When changing the gate here, mirror the change in sleep_gate.go
    -- (and vice versa) — see sleep_gate_test.go for the expectations.
    SELECT (
        (SELECT COUNT(DISTINCT source) FROM sleep_total_per_source) <= 1
        OR (
          SELECT COUNT(DISTINCT metric_name) FROM per_source
           WHERE source = (SELECT src FROM sleep_picked)
             AND metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake')
        ) = 5
        OR (
          SELECT COUNT(DISTINCT metric_name) FROM per_source
           WHERE source = (SELECT src FROM sleep_picked)
             AND metric_name IN ('sleep_total','sleep_unspecified')
        ) = 2
    ) AS ok
),
agg AS (
    SELECT
      metric_name,
      AVG(avg_val) AS avg_across_sources,
      -- preferred SUM source (non-sleep): Apple Watch > iPhone > MAX(any)
      COALESCE(
        MAX(sum_val) FILTER (WHERE source LIKE '%Ultra%' OR source LIKE '%Apple Watch%'),
        MAX(sum_val) FILTER (WHERE source LIKE '%iPhone%'),
        MAX(sum_val)
      ) AS sum_preferred,
      -- sleep: only when picked source covers all five stages. Otherwise
      -- emit NULL for every stage so the existing COALESCE in ON CONFLICT
      -- preserves the prior block atomically (no per-column drift).
      CASE WHEN (SELECT ok FROM sleep_picked_complete)
           THEN MAX(sum_val) FILTER (WHERE source = (SELECT src FROM sleep_picked))
           ELSE NULL
      END AS sum_sleep_resolved
    FROM per_source
    GROUP BY metric_name
)
INSERT INTO daily_scores
    (date, hrv_avg, rhr_avg, sleep_total, sleep_deep, sleep_rem, sleep_core,
     sleep_awake, sleep_unspecified, steps, calories, exercise_min, spo2_avg,
     vo2_avg, resp_avg, computed_at)
SELECT $1,
    MAX(avg_across_sources)  FILTER (WHERE metric_name='heart_rate_variability'),
    MAX(avg_across_sources)  FILTER (WHERE metric_name='resting_heart_rate'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_total'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_deep'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_rem'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_core'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_awake'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_unspecified'),
    MAX(sum_preferred)       FILTER (WHERE metric_name='step_count'),
    MAX(sum_preferred)       FILTER (WHERE metric_name='active_energy'),
    MAX(sum_preferred)       FILTER (WHERE metric_name='apple_exercise_time'),
    MAX(avg_across_sources)  FILTER (WHERE metric_name='blood_oxygen_saturation'),
    MAX(avg_across_sources)  FILTER (WHERE metric_name='vo2_max'),
    MAX(avg_across_sources)  FILTER (WHERE metric_name='respiratory_rate'),
    NOW()::TEXT
FROM agg
ON CONFLICT(date) DO UPDATE SET
    hrv_avg      = COALESCE(EXCLUDED.hrv_avg,      daily_scores.hrv_avg),
    rhr_avg      = COALESCE(EXCLUDED.rhr_avg,      daily_scores.rhr_avg),
    -- Sleep block writes are all-or-nothing per the atomicity gate
    -- (sleep_picked_complete). When the gate passes, EXCLUDED.sleep_total
    -- is non-NULL and we overwrite every stage column — even those that
    -- legitimately become NULL because the picked source is coarse-only
    -- (total + unspecified, no deep/rem/core/awake). Without this, a row
    -- previously populated from a staged device would keep its Apple
    -- Watch deep/REM next to RingConn coarse total — reintroducing the
    -- mixed-source corruption the gate is designed to prevent
    -- (CodeRabbit PR #73). Gate fails ⇒ EXCLUDED.sleep_total IS NULL
    -- ⇒ preserve the prior row as a whole.
    sleep_total       = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_total       ELSE daily_scores.sleep_total       END,
    sleep_deep        = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_deep        ELSE daily_scores.sleep_deep        END,
    sleep_rem         = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_rem         ELSE daily_scores.sleep_rem         END,
    sleep_core        = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_core        ELSE daily_scores.sleep_core        END,
    sleep_awake       = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_awake       ELSE daily_scores.sleep_awake       END,
    sleep_unspecified = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_unspecified ELSE daily_scores.sleep_unspecified END,
    steps        = COALESCE(EXCLUDED.steps,        daily_scores.steps),
    calories     = COALESCE(EXCLUDED.calories,     daily_scores.calories),
    exercise_min = COALESCE(EXCLUDED.exercise_min, daily_scores.exercise_min),
    spo2_avg     = COALESCE(EXCLUDED.spo2_avg,     daily_scores.spo2_avg),
    vo2_avg      = COALESCE(EXCLUDED.vo2_avg,      daily_scores.vo2_avg),
    resp_avg     = COALESCE(EXCLUDED.resp_avg,     daily_scores.resp_avg),
    computed_at  = EXCLUDED.computed_at`
	if _, err := s.pool.Exec(ctx, q, date); err != nil {
		log.Printf("upsertDailyForDate %s: %v", date, err)
	}
}

// BackfillAggregates rebuilds hourly_metrics from metric_points and
// daily_scores from hourly_metrics. If force=true all cache tables are
// truncated first; otherwise the last 48h are refreshed (catches re-synced
// data) and new data is appended.
func (s *DB) BackfillAggregates(force bool) error {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	ctx, cancel := longCtx()
	defer cancel()
	if force {
		// Wrap deletion in a transaction so crash doesn't leave empty tables.
		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin force clear: %w", err)
		}
		for _, tbl := range []string{"minute_metrics", "hourly_metrics"} {
			if _, err := tx.Exec(ctx, "DELETE FROM "+tbl); err != nil {
				tx.Rollback(ctx)
				return fmt.Errorf("clear %s: %w", tbl, err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit force clear: %w", err)
		}
		log.Println("cache tables cleared")
	}

	metrics, err := s.listMetricNames()
	if err != nil {
		return fmt.Errorf("list metrics: %w", err)
	}

	log.Printf("backfill aggregates: %d metrics", len(metrics))

	// Parallel per-metric hourly rebuild. Bounded at backfillConcurrency so
	// we don't exhaust the shared Postgres pool. With 2 tenants × 8-conn
	// pool × 2 workers = 32 in-flight at peak, comfortably below the
	// shared 50-conn ceiling (leaves room for authentik + others).
	const backfillConcurrency = 2
	sem := make(chan struct{}, backfillConcurrency)
	var wg sync.WaitGroup
	for _, m := range metrics {
		wg.Add(1)
		sem <- struct{}{}
		go func(m string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.buildHourlyMetric(m, aggFuncFor(m), force); err != nil {
				log.Printf("  hourly %s: %v", m, err)
			}
		}(m)
	}
	wg.Wait()

	// Level 2: hourly_metrics → daily_scores metric columns.
	if err := s.BuildDailyMetrics(force); err != nil {
		return fmt.Errorf("daily metrics: %w", err)
	}

	log.Println("backfill aggregates done")
	return nil
}

// BuildDailyMetrics fills the metric columns of daily_scores from hourly_metrics.
// Existing readiness/score_version columns are not touched.
//
// Sleep stages are handled by a dedicated atomic block (buildDailySleepBlock)
// so all five phases come from one source per night and never drift across
// columns. The parallel per-column path is kept for AVG metrics and non-sleep
// SUM metrics, where mixing sources per metric does not produce nonsensical
// ratios.
func (s *DB) BuildDailyMetrics(force bool) error {
	type spec struct {
		col  string
		name string
	}
	specs := []spec{
		{"hrv_avg", "heart_rate_variability"},
		{"rhr_avg", "resting_heart_rate"},
		{"steps", "step_count"},
		{"calories", "active_energy"},
		{"exercise_min", "apple_exercise_time"},
		{"spo2_avg", "blood_oxygen_saturation"},
		{"vo2_avg", "vo2_max"},
		{"resp_avg", "respiratory_rate"},
	}

	const dailyConcurrency = 2
	sem := make(chan struct{}, dailyConcurrency)
	var wg sync.WaitGroup
	for _, sp := range specs {
		wg.Add(1)
		sem <- struct{}{}
		go func(sp spec) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.buildDailyMetricCol(sp.col, sp.name, force); err != nil {
				log.Printf("  daily %s (%s): %v", sp.col, sp.name, err)
			}
		}(sp)
	}
	wg.Wait()

	if err := s.buildDailySleepBlock(force); err != nil {
		// This path is the sole backfill writer for the sleep block now,
		// so a silent log would leave daily_scores.sleep_* stale without
		// the operator noticing. Fail the rebuild and let upstream retry.
		return fmt.Errorf("daily sleep block: %w", err)
	}

	// v2.2 baseline_hr_overnight backfill. Runs AFTER the sleep block so
	// `WakeTimeForDate` sees the freshest per-segment data. Per-date Go
	// helper rather than a single SQL because the window resolution
	// (longest asleep segment ±6h from midnight, last 3h) doesn't fit
	// cleanly into SQL without recursive CTEs. The buildBaselineHROvernightAll
	// helper logs+continues on individual-date errors — one bad night
	// shouldn't fail a months-long backfill.
	s.buildBaselineHROvernightAll(force)

	// v2.2 sustained_hr_load backfill. Reads everything the previous
	// passes wrote (sleep block for WakeTimeForDate's awake window,
	// baseline_hr_overnight is independent but recomputed for
	// consistency), so MUST run last in the chain.
	s.buildSustainedHRLoadAll(force)

	log.Printf("daily metrics filled (%d columns + sleep block + baseline_hr_overnight + sustained_hr_load)", len(specs))
	return nil
}

// buildBaselineHROvernightAll iterates over distinct dates present in
// daily_scores and (re)computes baseline_hr_overnight for each. Called
// from BackfillAggregates after the sleep block lands. With ~100k days
// across all tenants this is a few minutes of percentile_cont queries;
// most call sites use the per-date `upsertBaselineHROvernightForDate`
// from `UpsertRecentCache` instead.
//
// `force` is ignored for now — every call recomputes every row,
// because the column is small (REAL = 4 bytes) and a stale-detection
// gate would add complexity without a real win. Revisit if we ever
// see this loop dominate backfill time.
func (s *DB) buildBaselineHROvernightAll(force bool) {
	_ = force
	ctx, cancel := longCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `SELECT date FROM daily_scores ORDER BY date`)
	if err != nil {
		log.Printf("baseline_hr_overnight list: %v", err)
		return
	}
	var dates []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			continue
		}
		dates = append(dates, d)
	}
	if err := rows.Err(); err != nil {
		// Cursor / transport failure mid-iteration. Partial dates
		// slice would silently produce a misleading
		// "filled for N dates" log without the whole history
		// touched. Bail with the error logged instead.
		rows.Close()
		log.Printf("baseline_hr_overnight iter: %v", err)
		return
	}
	rows.Close()
	if len(dates) == 0 {
		return
	}
	loc := reportTZLocation()
	for _, d := range dates {
		s.upsertBaselineHROvernightForDate(d, loc)
	}
	log.Printf("baseline_hr_overnight filled for %d dates", len(dates))
}

func (s *DB) buildDailyMetricCol(col, metric string, force bool) error {
	ctx, cancel := longCtx()
	defer cancel()
	var fromClause string
	args := []any{metric}
	if !force {
		// Refresh last 7 days + fill new dates (catches late-arriving data
		// from offline devices like Apple Watch syncing after a week).
		var maxDate *string
		s.pool.QueryRow(ctx, `SELECT MAX(SUBSTRING(hour,1,10)) FROM hourly_metrics WHERE metric_name = $1`, metric).Scan(&maxDate)
		if maxDate == nil {
			return nil
		}
		refreshFrom := subtractDaysStr(*maxDate, 7)
		fromClause = "AND SUBSTRING(hour,1,10) >= $2"
		args = append(args, refreshFrom)
	}

	if isSleepMetric(metric) {
		// Sleep stages are handled atomically per night by
		// buildDailySleepBlock — should never reach this per-column path.
		log.Printf("buildDailyMetricCol called for sleep metric %s; ignoring (handled by buildDailySleepBlock)", metric)
		return nil
	}

	var query string
	if SumMetrics[metric] {
		// Non-sleep SUM metrics: Apple Watch > iPhone > other.
		srcPriority := `CASE
			WHEN source LIKE '%%Ultra%%' OR source LIKE '%%Apple Watch%%' THEN 1
			WHEN source LIKE '%%iPhone%%' THEN 2
			ELSE 3 END`
		query = fmt.Sprintf(`
			SELECT day, source_total FROM (
				SELECT day, source_total,
				       ROW_NUMBER() OVER (PARTITION BY day ORDER BY src_rank, source_total DESC) AS rn
				FROM (
					SELECT SUBSTRING(hour,1,10) AS day, source, SUM(avg_val) AS source_total,
					       %s AS src_rank
					FROM hourly_metrics
					WHERE metric_name = $1 %s
					GROUP BY SUBSTRING(hour,1,10), source
				) sub
			) ranked WHERE rn = 1
			ORDER BY day`, srcPriority, fromClause)
	} else {
		query = fmt.Sprintf(`
			SELECT SUBSTRING(hour,1,10), AVG(avg_val)
			FROM hourly_metrics
			WHERE metric_name = $1 %s
			GROUP BY SUBSTRING(hour,1,10)`, fromClause)
	}

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var date string
		var val float64
		if rows.Scan(&date, &val) != nil {
			continue
		}
		s.pool.Exec(ctx, fmt.Sprintf(`
			INSERT INTO daily_scores (date, %s, computed_at)
			VALUES ($1, $2, NOW()::TEXT)
			ON CONFLICT(date) DO UPDATE SET %s = excluded.%s, computed_at = excluded.computed_at`,
			col, col, col), date, val)
	}
	return rows.Err()
}

// isSleepMetric returns true for sleep_* metrics that may have both a midnight
// summary record and individual fragment records from different data sources.
func isSleepMetric(metric string) bool {
	return strings.HasPrefix(metric, "sleep_")
}

// buildDailySleepBlock writes the five sleep_* columns of daily_scores
// atomically per night: pick ONE source per day (driven by sleep_total
// totals + cross-validation rule), and only commit all five stages if
// the picked source covers the full set. Otherwise emit NULL for every
// stage so the existing ON CONFLICT COALESCE preserves the prior block
// as a unit. This mirrors upsertDailyForDate's logic but operates on a
// date range so the force-backfill path doesn't reopen the mixed-source
// row this whole subsystem is trying to eliminate.
//
// `force=true` rebuilds every day in hourly_metrics; otherwise the last
// 7 days from the most recent sleep_total day are refreshed (matches the
// per-column buildDailyMetricCol cadence so late-arriving Apple Watch
// fragments still land on the right night).
func (s *DB) buildDailySleepBlock(force bool) error {
	ctx, cancel := longCtx()
	defer cancel()

	var fromClause string
	var args []any
	if !force {
		var maxDate *string
		s.pool.QueryRow(ctx,
			`SELECT MAX(SUBSTRING(hour,1,10)) FROM hourly_metrics WHERE metric_name = 'sleep_total'`,
		).Scan(&maxDate)
		if maxDate == nil {
			return nil
		}
		refreshFrom := subtractDaysStr(*maxDate, 7)
		fromClause = "AND SUBSTRING(hour,1,10) >= $1"
		args = append(args, refreshFrom)
	}

	// Multi-day variant of upsertDailyForDate's sleep block. Note: the
	// per-day source pick is inlined rather than calling
	// sleepCrossValidationPickSourceExpr because the helper assumes a
	// flat (source, value) table — multi-day needs GROUP BY day. Threshold
	// constants (1.0h floor, 1.4× divergence) and source priority
	// (Apple Watch > RingConn > MAX) are duplicated here; keep them in
	// lockstep with the helper above. (Rule: any threshold change touches
	// both this function AND sleepCrossValidationPickSourceExpr.)
	q := `
WITH per_source AS (
    SELECT SUBSTRING(hour,1,10) AS day, metric_name, source,
           SUM(avg_val) AS sum_val
    FROM hourly_metrics
    WHERE metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake','sleep_unspecified')
      ` + fromClause + `
    GROUP BY SUBSTRING(hour,1,10), metric_name, source
),
sleep_total_per_day AS (
    SELECT day, source, sum_val FROM per_source WHERE metric_name = 'sleep_total'
),
day_stats AS (
    SELECT day,
           COUNT(*)        AS n_sources,
           MIN(sum_val)    AS min_total,
           MAX(sum_val)    AS max_total
    FROM sleep_total_per_day
    GROUP BY day
),
-- Both pickers add source ASC as a stable tiebreaker so equal totals
-- always resolve to the same row across reruns; without it, DISTINCT ON
-- can return either matching row and daily_scores would drift.
priority_pick AS (
    SELECT DISTINCT ON (day) day, source
    FROM sleep_total_per_day
    ORDER BY day,
        CASE WHEN source LIKE '%Ultra%' OR source LIKE '%Apple Watch%' THEN 0
             WHEN source LIKE '%RingConn%'                              THEN 1
             ELSE                                                            2 END,
        sum_val DESC,
        source ASC
),
min_pick AS (
    SELECT DISTINCT ON (day) day, source
    FROM sleep_total_per_day
    ORDER BY day, sum_val ASC, source ASC
),
sleep_picked AS (
    SELECT s.day,
        CASE WHEN s.n_sources > 1 AND s.min_total > 1.0
                  AND s.max_total > s.min_total * 1.4
             THEN m.source
             ELSE p.source
        END AS src
    FROM day_stats s
    LEFT JOIN priority_pick p ON p.day = s.day
    LEFT JOIN min_pick      m ON m.day = s.day
),
-- Atomicity gate: when MULTIPLE sources contributed sleep_total for the
-- night, demand the picked source has all five stages so we don't write
-- a NULL stage that ON CONFLICT COALESCE then fills from a prior row
-- with a DIFFERENT source (the mixing bug PR #26 closed). With a single
-- source there is no mixing risk, so trust it as-is — strict completeness
-- would erase a real night just because one stage happened to be 0
-- (HAE Apple Watch nights with sleep_awake = 0 hit this: hourly_metrics
-- filter qty > 0 drops the awake row, so the source has only 4 of 5
-- stages even though the night is fully recorded).
sleep_complete AS (
    -- Conditional gate (mirrors upsertDailyForDate):
    --   single source                              → trust as-is
    --   multi-source + all 5 stages from picked    → complete (stage-tracking device)
    --   multi-source + total + unspecified picked  → complete (coarse-only device)
    -- KNOWN LIMITATION (Issue #77): a picked source emitting ONLY sleep_total
    -- (no stages, no unspecified) fails this gate by design — see the matching
    -- comment in upsertDailyForDate's sleep_picked_complete CTE for the
    -- reasoning. Both gates must stay in lockstep on this corner.
    --
    -- Regression coverage: see sleep_gate.go::EvaluateSleepPickedComplete
    -- — pure Go twin exercised by sleep_gate_test.go.
    SELECT sp.day, sp.src, (
        (SELECT COUNT(DISTINCT source) FROM sleep_total_per_day WHERE day = sp.day) <= 1
        OR (
          SELECT COUNT(DISTINCT metric_name) FROM per_source
           WHERE day = sp.day AND source = sp.src
             AND metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake')
        ) = 5
        OR (
          SELECT COUNT(DISTINCT metric_name) FROM per_source
           WHERE day = sp.day AND source = sp.src
             AND metric_name IN ('sleep_total','sleep_unspecified')
        ) = 2
    ) AS ok
    FROM sleep_picked sp
),
day_metric AS (
    SELECT p.day, p.metric_name,
        CASE WHEN sc.ok AND p.source = sc.src THEN p.sum_val END AS val
    FROM per_source p
    JOIN sleep_complete sc ON sc.day = p.day
)
INSERT INTO daily_scores (date, sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_awake, sleep_unspecified, computed_at)
SELECT day,
    MAX(val) FILTER (WHERE metric_name = 'sleep_total'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_deep'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_rem'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_core'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_awake'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_unspecified'),
    NOW()::TEXT
FROM day_metric
GROUP BY day
ON CONFLICT(date) DO UPDATE SET
    -- Atomic overwrite when the gate passed (EXCLUDED.sleep_total IS NOT
    -- NULL); preserve prior row as a whole otherwise. Mirrors the same
    -- pattern in upsertDailyForDate — coarse-only picks must clear
    -- stale stage columns from a prior staged-source write or we
    -- re-create the mixed-source row the gate exists to prevent
    -- (CodeRabbit PR #73).
    sleep_total       = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_total       ELSE daily_scores.sleep_total       END,
    sleep_deep        = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_deep        ELSE daily_scores.sleep_deep        END,
    sleep_rem         = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_rem         ELSE daily_scores.sleep_rem         END,
    sleep_core        = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_core        ELSE daily_scores.sleep_core        END,
    sleep_awake       = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_awake       ELSE daily_scores.sleep_awake       END,
    sleep_unspecified = CASE WHEN EXCLUDED.sleep_total IS NOT NULL THEN EXCLUDED.sleep_unspecified ELSE daily_scores.sleep_unspecified END,
    computed_at       = EXCLUDED.computed_at`

	if _, err := s.pool.Exec(ctx, q, args...); err != nil {
		return fmt.Errorf("daily sleep block: %w", err)
	}
	return nil
}

// buildHourlyMetric fills hourly_metrics for one metric directly from
// metric_points (skipping minute_metrics). Uses INSERT ... ON CONFLICT so
// re-synced data overwrites stale cache values.
//
// Sleep metrics get a DELETE-before-INSERT pass inside a transaction:
// the prior dedup policy emitted per-hour fragments and the new policy
// emits a single 00:00 summary row. ON CONFLICT only updates the row at
// the same (metric, hour, source) key, so without the DELETE the
// pre-existing fragment rows would survive and upsertDailyForDate would
// sum them with the new summary — double counting the night. The DELETE
// is scoped to the same date window as the INSERT (full history for
// force, last 7 days otherwise) so non-sleep refreshes are unaffected.
func (s *DB) buildHourlyMetric(metric, agg string, force bool) error {
	ctx, cancel := longCtx()
	defer cancel()
	var fromClause string
	var refreshFromOpt string
	args := []any{metric}
	if !force {
		// Refresh last 7 days + append new data (catches late-arriving data).
		var lastCached *string
		s.pool.QueryRow(ctx,
			`SELECT MAX(hour) FROM hourly_metrics WHERE metric_name = $1`, metric,
		).Scan(&lastCached)
		if lastCached != nil {
			refreshFromOpt = subtractDaysStr((*lastCached)[:10], 7)
			fromClause = "AND SUBSTRING(date,1,10) >= $2"
			args = append(args, refreshFromOpt)
		}
	}

	// Reuse the canonical clause so the force-rebuild path can't drift
	// from the live ingest path. sleepDedupClause returns "" for
	// non-sleep metrics, which preserves the previous behaviour.
	sleepDedup := sleepDedupClause(metric)

	var query string
	if agg == "SUM" {
		// SUM metrics: MAX within each minute (dedup re-syncs), then SUM per hour.
		query = fmt.Sprintf(`
			INSERT INTO hourly_metrics (metric_name, hour, source, avg_val, min_val, max_val)
			SELECT metric_name, hour, source, SUM(minute_max), MIN(minute_min), MAX(minute_max)
			FROM (
				SELECT metric_name, source,
				       SUBSTRING(date, 1, 13) || ':00' AS hour,
				       SUBSTRING(date, 1, 16) AS minute,
				       MAX(qty) AS minute_max, MIN(qty) AS minute_min
				FROM metric_points
				WHERE metric_name = $1 AND qty > 0 AND quality = 'ok' %s %s
				GROUP BY metric_name, source, SUBSTRING(date, 1, 13) || ':00', SUBSTRING(date, 1, 16)
			) sub
			GROUP BY metric_name, hour, source
			ON CONFLICT (metric_name, hour, source) DO UPDATE SET
				avg_val=EXCLUDED.avg_val, min_val=EXCLUDED.min_val, max_val=EXCLUDED.max_val`, sleepDedup, fromClause)
	} else {
		query = fmt.Sprintf(`
			INSERT INTO hourly_metrics (metric_name, hour, source, avg_val, min_val, max_val)
			SELECT metric_name,
			       SUBSTRING(date, 1, 13) || ':00' AS hour,
			       source,
			       AVG(qty), MIN(qty), MAX(qty)
			FROM metric_points
			WHERE metric_name = $1 AND qty > 0 AND quality = 'ok' %s %s
			GROUP BY metric_name, SUBSTRING(date, 1, 13) || ':00', source
			ON CONFLICT (metric_name, hour, source) DO UPDATE SET
				avg_val=EXCLUDED.avg_val, min_val=EXCLUDED.min_val, max_val=EXCLUDED.max_val`, sleepDedup, fromClause)
	}

	if !isSleepMetric(metric) {
		_, err := s.pool.Exec(ctx, query, args...)
		return err
	}

	// Sleep path: DELETE same window + INSERT in one transaction.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	delArgs := []any{metric}
	delWhere := ""
	if refreshFromOpt != "" {
		delArgs = append(delArgs, refreshFromOpt)
		delWhere = " AND SUBSTRING(hour,1,10) >= $2"
	}
	if _, err := tx.Exec(ctx, `DELETE FROM hourly_metrics WHERE metric_name = $1`+delWhere, delArgs...); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// subtractDaysStr subtracts N days from a YYYY-MM-DD string.
func subtractDaysStr(dateStr string, days int) string {
	// Reuse the subtractDays from briefing.go via simple inline logic.
	t, err := parseDate(dateStr)
	if err != nil {
		return dateStr
	}
	return t.AddDate(0, 0, -days).Format("2006-01-02")
}
