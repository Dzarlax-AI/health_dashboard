package storage

import (
	"context"
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
// total for SUM metrics. This correctly handles both data patterns:
//
//   - Health Auto Export: pipe-separated sources ("Watch|iPhone|Ring")
//     represent different HealthKit dedup levels of the same data.
//     The source with the most devices has the most complete total.
//   - Apple Health XML import: single-device sources overlap.
//     MAX picks the device with the most data.
//
// In both cases, MAX across per-source totals is the correct strategy.
func sumCombineExpr(valCol string) string {
	return "MAX(" + valCol + ")"
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
	rows, err := s.pool.Query(context.Background(), `SELECT DISTINCT metric_name FROM metric_points ORDER BY metric_name`)
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
				GROUP BY metric_name, source, minute
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
			GROUP BY metric_name, hour, source
			ON CONFLICT (metric_name, hour, source) DO UPDATE SET
				avg_val=EXCLUDED.avg_val, min_val=EXCLUDED.min_val, max_val=EXCLUDED.max_val`, sleepDedup)
	}

	if _, err := s.pool.Exec(context.Background(), query, metric, date); err != nil {
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

	ctx := context.Background()
	for _, sp := range specs {
		var val float64
		var err error
		if SumMetrics[sp.name] {
			combineVal := sumCombineExpr("avg_val")
			err = s.pool.QueryRow(ctx, fmt.Sprintf(`
				SELECT COALESCE(SUM(hour_val), 0) FROM (
					SELECT hour, %s AS hour_val
					FROM hourly_metrics
					WHERE metric_name=$1 AND SUBSTRING(hour,1,10)=$2
					GROUP BY hour
				) sub`, combineVal), sp.name, date).Scan(&val)
		} else {
			err = s.pool.QueryRow(ctx, `
				SELECT COALESCE(AVG(avg_val), 0)
				FROM hourly_metrics
				WHERE metric_name=$1 AND SUBSTRING(hour,1,10)=$2`,
				sp.name, date).Scan(&val)
		}
		if err != nil || val == 0 {
			continue
		}
		s.pool.Exec(ctx, fmt.Sprintf(`
			INSERT INTO daily_scores (date, %s, computed_at)
			VALUES ($1, $2, NOW()::TEXT)
			ON CONFLICT(date) DO UPDATE SET %s = excluded.%s, computed_at = excluded.computed_at`,
			sp.col, sp.col, sp.col), date, val)
	}
}

// BackfillAggregates rebuilds hourly_metrics from metric_points and
// daily_scores from hourly_metrics. If force=true all cache tables are
// truncated first; otherwise the last 48h are refreshed (catches re-synced
// data) and new data is appended.
func (s *DB) BackfillAggregates(force bool) error {
	ctx := context.Background()
	if force {
		for _, tbl := range []string{"minute_metrics", "hourly_metrics"} {
			if _, err := s.pool.Exec(ctx, "DELETE FROM "+tbl); err != nil {
				return fmt.Errorf("clear %s: %w", tbl, err)
			}
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
	ctx := context.Background()
	var fromClause string
	if !force {
		// Refresh last 2 days + fill new dates (catches re-synced data).
		var maxDate *string
		s.pool.QueryRow(ctx, `SELECT MAX(SUBSTRING(hour,1,10)) FROM hourly_metrics WHERE metric_name = $1`, metric).Scan(&maxDate)
		if maxDate == nil {
			return nil
		}
		refreshFrom := subtractDaysStr(*maxDate, 2)
		fromClause = fmt.Sprintf("AND SUBSTRING(hour,1,10) >= '%s'", refreshFrom)
	}

	var query string
	if SumMetrics[metric] {
		combineVal := sumCombineExpr("avg_val")
		query = fmt.Sprintf(`
			SELECT day, SUM(hour_val) FROM (
				SELECT SUBSTRING(hour,1,10) AS day, hour, %s AS hour_val
				FROM hourly_metrics
				WHERE metric_name = $1 %s
				GROUP BY hour
			) sub
			GROUP BY day`, combineVal, fromClause)
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
	ctx := context.Background()
	var fromClause string
	if !force {
		// Refresh last 48h + append new data (catches re-synced values).
		var lastCached *string
		s.pool.QueryRow(ctx,
			`SELECT MAX(hour) FROM hourly_metrics WHERE metric_name = $1`, metric,
		).Scan(&lastCached)
		if lastCached != nil {
			refreshFrom := subtractDaysStr((*lastCached)[:10], 2)
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
				GROUP BY metric_name, source, minute
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
			GROUP BY metric_name, hour, source
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
