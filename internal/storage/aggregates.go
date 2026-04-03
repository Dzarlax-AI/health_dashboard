package storage

import (
	"fmt"
	"log"
	"strings"
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
// Cross-validation: when multiple sources exist and MAX/MIN differ by >40%,
// the higher value is likely an outlier — take MIN instead of the preferred source.
const preferredSleepSourceSQL = `
	SELECT CASE
		WHEN (SELECT COUNT(DISTINCT source) FROM source_totals) > 1
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
// dates directly from metric_points. Called inline after POST /health so the
// cache is always fresh — no "hole" between invalidation and backfill.
// Typically takes <100ms for a single day.
func (s *DB) UpsertRecentCache(dates []string) {
	if len(dates) == 0 {
		return
	}
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()

	metrics, err := s.listMetricNames()
	if err != nil {
		log.Printf("upsert cache: list metrics: %v", err)
		return
	}
	for _, date := range dates {
		for _, m := range metrics {
			s.upsertHourlyForDate(m, date)
		}
		s.upsertDailyForDate(date)
	}
}

// upsertHourlyForDate rebuilds hourly_metrics for one metric+date from metric_points.
// Uses INSERT ... ON CONFLICT so stale values are overwritten.
func (s *DB) upsertHourlyForDate(metric, date string) {
	agg := aggFuncFor(metric)

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
		// For SUM metrics: MAX within each minute (dedup re-syncs),
		// then SUM across minutes within each hour.
		query = fmt.Sprintf(`
			INSERT INTO hourly_metrics (metric_name, hour, source, avg_val, min_val, max_val)
			SELECT metric_name, hour, source, SUM(minute_max), MIN(minute_min), MAX(minute_max)
			FROM (
				SELECT metric_name, source,
				       SUBSTRING(date, 1, 13) || ':00' AS hour,
				       SUBSTRING(date, 1, 16) AS minute,
				       MAX(qty) AS minute_max, MIN(qty) AS minute_min
				FROM metric_points
				WHERE metric_name = $1 AND SUBSTRING(date,1,10) = $2 AND qty > 0 %s
				GROUP BY metric_name, source, SUBSTRING(date, 1, 13) || ':00', SUBSTRING(date, 1, 16)
			) sub
			GROUP BY metric_name, hour, source
			ON CONFLICT (metric_name, hour, source) DO UPDATE SET
				avg_val=EXCLUDED.avg_val, min_val=EXCLUDED.min_val, max_val=EXCLUDED.max_val`, sleepDedup)
	} else {
		query = fmt.Sprintf(`
			INSERT INTO hourly_metrics (metric_name, hour, source, avg_val, min_val, max_val)
			SELECT metric_name,
			       SUBSTRING(date, 1, 13) || ':00' AS hour,
			       source,
			       AVG(qty), MIN(qty), MAX(qty)
			FROM metric_points
			WHERE metric_name = $1 AND SUBSTRING(date,1,10) = $2 AND qty > 0 %s
			GROUP BY metric_name, SUBSTRING(date, 1, 13) || ':00', source
			ON CONFLICT (metric_name, hour, source) DO UPDATE SET
				avg_val=EXCLUDED.avg_val, min_val=EXCLUDED.min_val, max_val=EXCLUDED.max_val`, sleepDedup)
	}

	ctx, cancel := longCtx()
	defer cancel()
	if _, err := s.pool.Exec(ctx, query, metric, date); err != nil {
		log.Printf("upsert hourly %s/%s: %v", metric, date, err)
	}
}

// upsertDailyForDate rebuilds daily_scores metric columns for one date
// from hourly_metrics. Readiness is not touched (computed separately).
func (s *DB) upsertDailyForDate(date string) {
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

	ctx, cancel := longCtx()
	defer cancel()

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		log.Printf("upsertDailyForDate begin tx: %v", err)
		return
	}
	defer tx.Rollback(ctx)

	for _, sp := range specs {
		var val float64
		var qErr error
		if SumMetrics[sp.name] {
			qErr = tx.QueryRow(ctx, `
				WITH source_totals AS (
					SELECT source, SUM(avg_val) AS source_total
					FROM hourly_metrics
					WHERE metric_name=$1 AND SUBSTRING(hour,1,10)=$2
					GROUP BY source
				) `+preferredSourceSQL, sp.name, date).Scan(&val)
		} else {
			qErr = tx.QueryRow(ctx, `
				SELECT COALESCE(AVG(avg_val), 0)
				FROM hourly_metrics
				WHERE metric_name=$1 AND SUBSTRING(hour,1,10)=$2`,
				sp.name, date).Scan(&val)
		}
		if qErr != nil {
			continue
		}
		if _, err := tx.Exec(ctx, fmt.Sprintf(`
			INSERT INTO daily_scores (date, %s, computed_at)
			VALUES ($1, $2, NOW()::TEXT)
			ON CONFLICT(date) DO UPDATE SET %s = excluded.%s, computed_at = excluded.computed_at`,
			sp.col, sp.col, sp.col), date, val); err != nil {
			log.Printf("upsertDailyForDate exec %s/%s: %v", sp.col, date, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		log.Printf("upsertDailyForDate commit: %v", err)
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

	for _, m := range metrics {
		agg := aggFuncFor(m)
		if err := s.buildHourlyMetric(m, agg, force); err != nil {
			log.Printf("  hourly %s: %v", m, err)
		}
	}

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

	for _, sp := range specs {
		if err := s.buildDailyMetricCol(sp.col, sp.name, force); err != nil {
			log.Printf("  daily %s (%s): %v", sp.col, sp.name, err)
		}
	}
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
		// Pick best source per day using priority (sleep: RingConn > Watch; other: Watch > iPhone).
		srcPriority := `CASE
			WHEN source LIKE '%%Ultra%%' OR source LIKE '%%Apple Watch%%' THEN 1
			WHEN source LIKE '%%iPhone%%' THEN 2
			ELSE 3 END`
		if isSleepMetric(metric) {
			srcPriority = `CASE
				WHEN source LIKE '%%RingConn%%' THEN 1
				WHEN source LIKE '%%Ultra%%' OR source LIKE '%%Apple Watch%%' THEN 2
				ELSE 3 END`
		}
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
