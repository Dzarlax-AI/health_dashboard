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
}

// sleepDedupClause returns a SQL WHERE clause that excludes midnight summary
// records (00:00:00) when real sleep fragments exist for the same day+source.
// Returns empty string for non-sleep metrics.
func sleepDedupClause(metric string) string {
	if !isSleepMetric(metric) {
		return ""
	}
	return `AND NOT (
		SUBSTRING(date, 12, 8) = '00:00:00'
		AND EXISTS (
			SELECT 1 FROM metric_points p2
			WHERE p2.metric_name = metric_points.metric_name
			  AND SUBSTRING(p2.date, 1, 10) = SUBSTRING(metric_points.date, 1, 10)
			  AND p2.source = metric_points.source
			  AND SUBSTRING(p2.date, 12, 8) != '00:00:00'
			  AND p2.qty > 0
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
	s.cacheMu.Lock()
	for _, date := range dates {
		s.upsertHourlyAvgForDate(date)
		s.upsertHourlySumForDate(date)
		s.upsertHourlySleepForDate(date)
		s.upsertDailyForDate(date)
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
// One SQL statement using a NOT EXISTS subquery for midnight-summary dedup.
func (s *DB) upsertHourlySleepForDate(date string) {
	ctx, cancel := longCtx()
	defer cancel()
	const q = `
		INSERT INTO hourly_metrics (metric_name, hour, source, avg_val, min_val, max_val)
		SELECT metric_name, hour, source,
		       SUM(minute_max), MIN(minute_min), MAX(minute_max)
		FROM (
			SELECT mp.metric_name, mp.source,
			       SUBSTRING(mp.date, 1, 13) || ':00' AS hour,
			       SUBSTRING(mp.date, 1, 16) AS minute,
			       MAX(mp.qty) AS minute_max, MIN(mp.qty) AS minute_min
			FROM metric_points mp
			WHERE SUBSTRING(mp.date,1,10) = $1
			  AND mp.qty > 0
			  AND mp.quality = 'ok'
			  AND mp.metric_name LIKE 'sleep\_%' ESCAPE '\'
			  AND NOT (
			      SUBSTRING(mp.date, 12, 8) = '00:00:00'
			      AND EXISTS (
			          SELECT 1 FROM metric_points p2
			          WHERE p2.metric_name = mp.metric_name
			            AND SUBSTRING(p2.date,1,10) = SUBSTRING(mp.date,1,10)
			            AND p2.source = mp.source
			            AND SUBSTRING(p2.date,12,8) <> '00:00:00'
			            AND p2.qty > 0
			      )
			  )
			GROUP BY mp.metric_name, mp.source,
			         SUBSTRING(mp.date, 1, 13) || ':00',
			         SUBSTRING(mp.date, 1, 16)
		) sub
		GROUP BY metric_name, hour, source
		ON CONFLICT (metric_name, hour, source) DO UPDATE SET
			avg_val=EXCLUDED.avg_val, min_val=EXCLUDED.min_val, max_val=EXCLUDED.max_val`
	if _, err := s.pool.Exec(ctx, q, date); err != nil {
		log.Printf("upsert hourly sleep %s: %v", date, err)
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
-- Atomicity gate: only commit the picked source's stages when ALL five
-- are present. Otherwise (e.g. RingConn writes a sleep_total row but no
-- phases) we'd write a NULL for the missing stages, and ON CONFLICT
-- COALESCE would preserve a prior row's stages — recreating exactly the
-- mixed-source row this PR is trying to eliminate. By forcing every
-- stage to NULL when incomplete, COALESCE preserves the previous block
-- as a unit instead of column-by-column.
sleep_picked_complete AS (
    SELECT (
      SELECT COUNT(DISTINCT metric_name) FROM per_source
       WHERE source = (SELECT src FROM sleep_picked)
         AND metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake')
    ) = 5 AS ok
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
     sleep_awake, steps, calories, exercise_min, spo2_avg, vo2_avg, resp_avg,
     computed_at)
SELECT $1,
    MAX(avg_across_sources)  FILTER (WHERE metric_name='heart_rate_variability'),
    MAX(avg_across_sources)  FILTER (WHERE metric_name='resting_heart_rate'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_total'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_deep'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_rem'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_core'),
    MAX(sum_sleep_resolved)  FILTER (WHERE metric_name='sleep_awake'),
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
    sleep_total  = COALESCE(EXCLUDED.sleep_total,  daily_scores.sleep_total),
    sleep_deep   = COALESCE(EXCLUDED.sleep_deep,   daily_scores.sleep_deep),
    sleep_rem    = COALESCE(EXCLUDED.sleep_rem,    daily_scores.sleep_rem),
    sleep_core   = COALESCE(EXCLUDED.sleep_core,   daily_scores.sleep_core),
    sleep_awake  = COALESCE(EXCLUDED.sleep_awake,  daily_scores.sleep_awake),
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

	log.Printf("daily metrics filled (%d columns + sleep block)", len(specs))
	return nil
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
    WHERE metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake')
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
sleep_complete AS (
    SELECT sp.day, sp.src, (
      SELECT COUNT(DISTINCT metric_name) FROM per_source
       WHERE day = sp.day AND source = sp.src
         AND metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake')
    ) = 5 AS ok
    FROM sleep_picked sp
),
day_metric AS (
    SELECT p.day, p.metric_name,
        CASE WHEN sc.ok AND p.source = sc.src THEN p.sum_val END AS val
    FROM per_source p
    JOIN sleep_complete sc ON sc.day = p.day
)
INSERT INTO daily_scores (date, sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_awake, computed_at)
SELECT day,
    MAX(val) FILTER (WHERE metric_name = 'sleep_total'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_deep'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_rem'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_core'),
    MAX(val) FILTER (WHERE metric_name = 'sleep_awake'),
    NOW()::TEXT
FROM day_metric
GROUP BY day
ON CONFLICT(date) DO UPDATE SET
    sleep_total = COALESCE(EXCLUDED.sleep_total, daily_scores.sleep_total),
    sleep_deep  = COALESCE(EXCLUDED.sleep_deep,  daily_scores.sleep_deep),
    sleep_rem   = COALESCE(EXCLUDED.sleep_rem,   daily_scores.sleep_rem),
    sleep_core  = COALESCE(EXCLUDED.sleep_core,  daily_scores.sleep_core),
    sleep_awake = COALESCE(EXCLUDED.sleep_awake, daily_scores.sleep_awake),
    computed_at = EXCLUDED.computed_at`

	if _, err := s.pool.Exec(ctx, q, args...); err != nil {
		return fmt.Errorf("daily sleep block: %w", err)
	}
	return nil
}

// buildHourlyMetric fills hourly_metrics for one metric directly from
// metric_points (skipping minute_metrics). Uses INSERT ... ON CONFLICT so
// re-synced data overwrites stale cache values.
func (s *DB) buildHourlyMetric(metric, agg string, force bool) error {
	ctx, cancel := longCtx()
	defer cancel()
	var fromClause string
	if !force {
		// Refresh last 7 days + append new data (catches late-arriving data).
		var lastCached *string
		s.pool.QueryRow(ctx,
			`SELECT MAX(hour) FROM hourly_metrics WHERE metric_name = $1`, metric,
		).Scan(&lastCached)
		if lastCached != nil {
			refreshFrom := subtractDaysStr((*lastCached)[:10], 7)
			fromClause = fmt.Sprintf("AND SUBSTRING(date,1,10) >= '%s'", refreshFrom)
		}
	}

	sleepDedup := ""
	if isSleepMetric(metric) {
		sleepDedup = `AND NOT (
			SUBSTRING(date, 12, 8) = '00:00:00'
			AND EXISTS (
				SELECT 1 FROM metric_points p2
				WHERE p2.metric_name = metric_points.metric_name
				  AND SUBSTRING(p2.date, 1, 10) = SUBSTRING(metric_points.date, 1, 10)
				  AND p2.source = metric_points.source
				  AND SUBSTRING(p2.date, 12, 8) != '00:00:00'
				  AND p2.qty > 0
			)
		)`
	}

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

	_, err := s.pool.Exec(ctx, query, metric)
	return err
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
