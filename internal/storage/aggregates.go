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

// preferredSleepSourceSQL picks the best source for sleep metrics.
// Priority: Apple Watch > RingConn > other.
// Apple Watch is better validated against polysomnography; RingConn tends to
// overestimate deep sleep and occasionally reports wildly inflated totals.
//
// Cross-validation: when multiple sources exist AND each registered a
// non-trivial sleep total (MIN > 1h), AND MAX/MIN differ by >40%, the higher
// value is likely an outlier — take MIN instead of the preferred source.
//
// The MIN > 1h gate is critical: without it, a RingConn daily-summary record
// of 0.13h (essentially a stub) would always satisfy "MAX > MIN*1.4" and the
// CASE would return that 0.13h, throwing away Apple Watch's valid 7+h night.
// Cross-validation only makes sense when both sources have legitimate data
// to compare — otherwise fall through to the source-priority COALESCE.
const preferredSleepSourceSQL = `
	SELECT CASE
		WHEN (SELECT COUNT(DISTINCT source) FROM source_totals) > 1
		 AND (SELECT MIN(source_total) FROM source_totals) > 1.0
		 AND (SELECT MAX(source_total) FROM source_totals) >
		     (SELECT MIN(source_total) FROM source_totals) * 1.4
		THEN (SELECT MIN(source_total) FROM source_totals)
		ELSE COALESCE(
			(SELECT source_total FROM source_totals
			 WHERE source LIKE '%Ultra%' OR source LIKE '%Apple Watch%'
			 ORDER BY source_total DESC LIMIT 1),
			(SELECT source_total FROM source_totals
			 WHERE source LIKE '%RingConn%'
			 ORDER BY source_total DESC LIMIT 1),
			(SELECT MAX(source_total) FROM source_totals)
		)
	END`

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
	//   SUM (sleep)  → cross-validated source pick (Apple Watch > RingConn,
	//                  fall back to MIN if sources diverge >40%)
	//   SUM (other)  → preferred source pick (Apple Watch > iPhone > MAX)
	//
	// We compute all three resolved values per (metric, day) in one CTE and
	// project the right one per metric column with a CASE.
	const q = `
WITH per_source AS (
    SELECT metric_name, source,
           AVG(avg_val) AS avg_val,
           SUM(avg_val) AS sum_val
    FROM hourly_metrics
    WHERE SUBSTRING(hour,1,10) = $1
    GROUP BY metric_name, source
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
      -- sleep cross-validation: if sources diverge >40%, take MIN
      CASE
        WHEN COUNT(DISTINCT source) > 1
             AND MAX(sum_val) > MIN(sum_val) * 1.4
        THEN MIN(sum_val)
        ELSE COALESCE(
          MAX(sum_val) FILTER (WHERE source LIKE '%Ultra%' OR source LIKE '%Apple Watch%'),
          MAX(sum_val) FILTER (WHERE source LIKE '%RingConn%'),
          MAX(sum_val)
        )
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
func (s *DB) BuildDailyMetrics(force bool) error {
	type spec struct {
		col  string
		name string
	}
	specs := []spec{
		{"hrv_avg", "heart_rate_variability"},
		{"rhr_avg", "resting_heart_rate"},
		{"sleep_total", "sleep_total"},
		{"sleep_deep", "sleep_deep"},
		{"sleep_rem", "sleep_rem"},
		{"sleep_core", "sleep_core"},
		{"sleep_awake", "sleep_awake"},
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
	log.Printf("daily metrics filled (%d columns)", len(specs))
	return nil
}

func (s *DB) buildDailyMetricCol(col, metric string, force bool) error {
	ctx, cancel := longCtx()
	defer cancel()
	var fromClause string
	if !force {
		// Refresh last 7 days + fill new dates (catches late-arriving data
		// from offline devices like Apple Watch syncing after a week).
		var maxDate *string
		s.pool.QueryRow(ctx, `SELECT MAX(SUBSTRING(hour,1,10)) FROM hourly_metrics WHERE metric_name = $1`, metric).Scan(&maxDate)
		if maxDate == nil {
			return nil
		}
		refreshFrom := subtractDaysStr(*maxDate, 7)
		fromClause = fmt.Sprintf("AND SUBSTRING(hour,1,10) >= '%s'", refreshFrom)
	}

	var query string
	if SumMetrics[metric] {
		if isSleepMetric(metric) {
			// Sleep: Apple Watch > RingConn, with cross-validation.
			// If sources diverge by >40%, the higher value is likely an outlier — take MIN.
			query = fmt.Sprintf(`
				SELECT day,
				    CASE
				        WHEN COUNT(*) > 1 AND MAX(source_total) > MIN(source_total) * 1.4
				        THEN MIN(source_total)
				        ELSE COALESCE(
				            MAX(CASE WHEN source LIKE '%%%%Ultra%%%%' OR source LIKE '%%%%Apple Watch%%%%' THEN source_total END),
				            MAX(CASE WHEN source LIKE '%%%%RingConn%%%%' THEN source_total END),
				            MAX(source_total)
				        )
				    END AS val
				FROM (
				    SELECT SUBSTRING(hour,1,10) AS day, source, SUM(avg_val) AS source_total
				    FROM hourly_metrics
				    WHERE metric_name = $1 %s
				    GROUP BY SUBSTRING(hour,1,10), source
				) sub
				GROUP BY day
				ORDER BY day`, fromClause)
		} else {
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
		}
	} else {
		query = fmt.Sprintf(`
			SELECT SUBSTRING(hour,1,10), AVG(avg_val)
			FROM hourly_metrics
			WHERE metric_name = $1 %s
			GROUP BY SUBSTRING(hour,1,10)`, fromClause)
	}

	rows, err := s.pool.Query(ctx, query, metric)
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
				WHERE metric_name = $1 AND qty > 0 %s %s
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
			WHERE metric_name = $1 AND qty > 0 %s %s
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
