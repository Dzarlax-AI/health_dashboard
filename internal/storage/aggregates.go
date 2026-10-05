package storage

import (
	"context"
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
	watch := sourcePriorityCondition("source", sleepSourcePriority[0])
	ringConn := sourcePriorityCondition("source", sleepSourcePriority[1])
	return `CASE
		WHEN COUNT(*) > 1 AND MIN(` + valCol + `) > ` + sqlPolicyNumber(sleepCrossValidationMinHours) + `
		 AND MAX(` + valCol + `) > MIN(` + valCol + `) * ` + sqlPolicyNumber(sleepCrossValidationDivergence) + `
		THEN MIN(` + valCol + `)
		ELSE COALESCE(
		    MAX(CASE WHEN ` + watch + ` THEN ` + valCol + ` END),
		    MAX(CASE WHEN ` + ringConn + ` THEN ` + valCol + ` END),
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
	watch := sourcePriorityCondition("source", sleepSourcePriority[0])
	ringConn := sourcePriorityCondition("source", sleepSourcePriority[1])
	// Tiebreak by `source ASC` so two rows with identical totals always
	// resolve to the same pick across reruns. Without this, Postgres can
	// return either row when sums tie, and the picked source flips
	// between backfills — causing daily_scores.sleep_* to drift even
	// with no new data.
	return `(
		CASE
			WHEN (SELECT COUNT(*) FROM ` + table + `) > 1
			 AND (SELECT MIN(` + valCol + `) FROM ` + table + `) > ` + sqlPolicyNumber(sleepCrossValidationMinHours) + `
			 AND (SELECT MAX(` + valCol + `) FROM ` + table + `) >
			     (SELECT MIN(` + valCol + `) FROM ` + table + `) * ` + sqlPolicyNumber(sleepCrossValidationDivergence) + `
			THEN (SELECT source FROM ` + table + ` ORDER BY ` + valCol + ` ASC, source ASC LIMIT 1)
			ELSE COALESCE(
				(SELECT source FROM ` + table + `
				  WHERE ` + watch + `
				  ORDER BY ` + valCol + ` DESC, source ASC LIMIT 1),
				(SELECT source FROM ` + table + `
				  WHERE ` + ringConn + `
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
var preferredSourceSQL = `
	SELECT COALESCE(
		(SELECT source_total FROM source_totals
		 WHERE ` + sourcePriorityCondition("source", sumSourcePriority[0]) + `
		 ORDER BY source_total DESC LIMIT 1),
		(SELECT source_total FROM source_totals
		 WHERE ` + sourcePriorityCondition("source", sumSourcePriority[1]) + `
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
func (s *DB) UpsertRecentCache(dates []string, recomputeReadiness bool) error {
	if len(dates) == 0 {
		return nil
	}
	finishRefresh := s.BeginDashboardRefresh()
	defer finishRefresh()
	// Tenant TZ is read once per UpsertRecentCache pass and reused for
	// every date — REPORT_TZ doesn't change mid-process, and
	// `time.LoadLocation` allocates ~5 KB per call which adds up over a
	// 48-hour incremental window. Fallback to UTC inside
	// reportTZLocation matches energy_compute.go convention.
	loc := s.reportTZLocation()
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	captured, err := s.CaptureCacheDirty(context.Background(), dates)
	if err != nil {
		return fmt.Errorf("capture aggregate input generations: %w", err)
	}
	fail := func(err error) error {
		log.Printf("upsert recent cache: %v", err)
		return err
	}
	for _, date := range dates {
		if err := s.upsertHourlyAvgForDate(date); err != nil {
			return fail(fmt.Errorf("hourly avg %s: %w", date, err))
		}
		if err := s.upsertHourlySumForDate(date); err != nil {
			return fail(fmt.Errorf("hourly sum %s: %w", date, err))
		}
		if err := s.upsertHourlySleepForDate(date); err != nil {
			return fail(fmt.Errorf("hourly sleep %s: %w", date, err))
		}
		if err := s.upsertDailyForDate(date); err != nil {
			return fail(fmt.Errorf("daily scores %s: %w", date, err))
		}
		// Must run AFTER upsertDailyForDate — the latter creates the
		// daily_scores row that the baseline UPDATE targets. Cheap
		// enough to run inside the same critical section (one
		// percentile query + one UPDATE per date).
		if err := s.upsertBaselineHROvernightForDate(date, loc); err != nil {
			return fail(fmt.Errorf("overnight baseline %s: %w", date, err))
		}
		// v2.2 sustained_hr_load — depends on baseline_hr_overnight
		// being current AND on the personal HR baseline being
		// readable, so runs last in the per-date chain.
		if _, err := s.upsertSustainedHRLoadForDate(date, loc); err != nil {
			return fail(fmt.Errorf("sustained HR load %s: %w", date, err))
		}
	}

	if recomputeReadiness {
		earliest := dates[0]
		for _, d := range dates[1:] {
			if d < earliest {
				earliest = d
			}
		}
		if err := s.RecomputeReadinessSince(earliest); err != nil {
			return fail(err)
		}
	}
	ctx, cancel := longCtx()
	defer cancel()
	if err := s.refreshDashboardSnapshotLocked(ctx); err != nil {
		return fail(fmt.Errorf("dashboard snapshot: %w", err))
	}
	if s.cacheAppliedGenerations == nil {
		s.cacheAppliedGenerations = make(map[string]uint64)
	}
	for date, generation := range captured {
		s.cacheAppliedGenerations[date] = generation
	}
	return nil
}

// upsertHourlyAvgForDate rebuilds hourly_metrics for ALL non-sleep AVG metrics
// on `date` in a single SQL statement.
func (s *DB) upsertHourlyAvgForDate(date string) error {
	ctx, cancel := longCtx()
	defer cancel()
	q := aggregateSQL("live_hourly_avg")
	if _, err := s.pool.Exec(ctx, q, date, sumMetricSlice()); err != nil {
		return err
	}
	return nil
}

// upsertHourlySumForDate rebuilds hourly_metrics for non-sleep SUM metrics
// (steps, active_energy, etc.) — one SQL for all of them.
func (s *DB) upsertHourlySumForDate(date string) error {
	ctx, cancel := longCtx()
	defer cancel()
	q := aggregateSQL("live_hourly_sum")
	if _, err := s.pool.Exec(ctx, q, date, sumMetricSlice()); err != nil {
		return err
	}
	return nil
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
func (s *DB) upsertHourlySleepForDate(date string) error {
	ctx, cancel := longCtx()
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		DELETE FROM hourly_metrics
		WHERE SUBSTRING(hour,1,10) = $1
		AND metric_name LIKE 'sleep\_%' ESCAPE '\'`, date); err != nil {
		return fmt.Errorf("delete stale: %w", err)
	}

	q := aggregateSQLWith("live_hourly_sleep", map[string]string{"SLEEP_DEDUP": sleepDedupClause("sleep_total")})
	if _, err := tx.Exec(ctx, q, date); err != nil {
		return fmt.Errorf("insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
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
func (s *DB) upsertDailyForDate(date string) error {
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
	q := aggregateSQLWith("live_daily", map[string]string{
		"SLEEP_PICK_SOURCE":         sleepCrossValidationPickSourceExpr("sleep_total_per_source", "sum_val"),
		"SLEEP_TRADITIONAL_METRICS": sqlStringList(sleepTraditionalMetrics),
		"SLEEP_COARSE_METRICS":      sqlStringList(sleepCoarseMetrics),
		"SUM_WATCH_CONDITION":       sourcePriorityCondition("source", sumSourcePriority[0]),
		"SUM_IPHONE_CONDITION":      sourcePriorityCondition("source", sumSourcePriority[1]),
	})
	if _, err := s.pool.Exec(ctx, q, date); err != nil {
		return err
	}
	return nil
}

// BackfillAggregates rebuilds hourly_metrics from metric_points and
// daily_scores from hourly_metrics. If force=true all cache tables are
// truncated first; otherwise the last 48h are refreshed (catches re-synced
// data) and new data is appended.
func (s *DB) BackfillAggregates(force bool) error {
	finishRefresh := s.BeginDashboardRefresh()
	defer finishRefresh()
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
	var firstErr error
	var errMu sync.Mutex
	for _, m := range metrics {
		wg.Add(1)
		sem <- struct{}{}
		go func(m string) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.buildHourlyMetric(m, aggFuncFor(m), force); err != nil {
				log.Printf("  hourly %s: %v", m, err)
				errMu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("hourly %s: %w", m, err)
				}
				errMu.Unlock()
			}
		}(m)
	}
	wg.Wait()
	if firstErr != nil {
		return firstErr
	}

	// Level 2: hourly_metrics → daily_scores metric columns.
	if err := s.BuildDailyMetrics(force); err != nil {
		return fmt.Errorf("daily metrics: %w", err)
	}
	// The rebuild can exceed the five-minute context created at function entry.
	// Publish with a fresh deadline so a successful cache generation is always
	// made visible to request readers.
	snapshotCtx, cancelSnapshot := longCtx()
	defer cancelSnapshot()
	if err := s.refreshDashboardSnapshotLocked(snapshotCtx); err != nil {
		return fmt.Errorf("dashboard snapshot: %w", err)
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
	specs := dailyMetricSpecs

	const dailyConcurrency = 2
	sem := make(chan struct{}, dailyConcurrency)
	var wg sync.WaitGroup
	for _, sp := range specs {
		wg.Add(1)
		sem <- struct{}{}
		go func(sp aggregationMetricSpec) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := s.buildDailyMetricCol(sp.column, sp.metric, force); err != nil {
				log.Printf("  daily %s (%s): %v", sp.column, sp.metric, err)
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
	loc := s.reportTZLocation()
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

	if !allowedDailyMetricColumn(col) {
		return fmt.Errorf("unsupported daily metric column %q", col)
	}
	queryName := "legacy_daily_avg"
	if SumMetrics[metric] {
		queryName = "legacy_daily_sum"
	}
	replacements := map[string]string{"FROM_CLAUSE": fromClause}
	if queryName == "legacy_daily_sum" {
		replacements["SUM_SOURCE_RANK"] = legacySumSourceRankExpr()
	}
	query := aggregateSQLWith(queryName, replacements)

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
		s.pool.Exec(ctx,
			aggregateSQLWith("legacy_daily_upsert", map[string]string{"COLUMN": col}), date, val)
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

	// Multi-day variant of upsertDailyForDate's sleep block. The query
	// requires per-day grouping, so its thresholds and source-priority case
	// are supplied from the shared aggregation policy catalog.
	q := aggregateSQLWith("legacy_daily_sleep", map[string]string{
		"FROM_CLAUSE":               fromClause,
		"SLEEP_METRICS":             sqlStringList(sleepMetricNames),
		"SLEEP_TRADITIONAL_METRICS": sqlStringList(sleepTraditionalMetrics),
		"SLEEP_COARSE_METRICS":      sqlStringList(sleepCoarseMetrics),
		"SLEEP_PRIORITY_CASE":       sleepSourcePriorityCaseExpr("source"),
		"SLEEP_MIN_HOURS":           sqlPolicyNumber(sleepCrossValidationMinHours),
		"SLEEP_DIVERGENCE":          sqlPolicyNumber(sleepCrossValidationDivergence),
	})

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

	queryName := "legacy_hourly_avg"
	if agg == "SUM" {
		queryName = "legacy_hourly_sum"
	}
	query := aggregateSQLWith(queryName, map[string]string{
		"SLEEP_DEDUP": sleepDedup,
		"FROM_CLAUSE": fromClause,
	})

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
