package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// MetricSummary is returned by ListMetrics.
type MetricSummary struct {
	Name  string
	Units string
	Count int
	Min   string
	Max   string
}

// DataPoint is a single time-bucketed value returned by metric data queries.
type DataPoint struct {
	Date string  `json:"date"`
	Qty  float64 `json:"qty"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
}

// SourceDataPoints groups DataPoints by device source.
type SourceDataPoints struct {
	Source string      `json:"source"`
	Points []DataPoint `json:"points"`
}

// CardData is a single metric card value for the dashboard.
type CardData struct {
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
	Prev   float64 `json:"prev"` // previous day value for trend indicator
	Unit   string  `json:"unit"`
	Date   string  `json:"date"`
}

// DashboardResponse is returned by GetDashboard.
type DashboardResponse struct {
	Date             string     `json:"date"`
	LastUpdated      string     `json:"last_updated"`
	CacheState       string     `json:"cache_state"`
	CacheCompletedAt string     `json:"cache_completed_at,omitempty"`
	Cards            []CardData `json:"cards"`
}

const (
	dashboardCacheStateComplete    = "complete"
	dashboardCacheStateUpdating    = "updating"
	dashboardCacheStateUnavailable = "unavailable"
)

// dashboardCacheQuery is run only by the background cache writer. Requests
// read dashboard_cache_snapshots instead, so they never observe this mutable
// intermediate state or fall back to metric_points.
//
// SUM metrics keep the established source priority and sleep cross-validation;
// AVG metrics preserve their raw-sample average through hourly sample counts.
var dashboardCacheQuery = `
	WITH latest AS (
		SELECT MAX(SUBSTRING(hour, 1, 10)) AS today
		FROM hourly_metrics
	), days AS (
		SELECT today,
		       (SELECT MAX(SUBSTRING(hour, 1, 10))
		        FROM hourly_metrics
		        WHERE SUBSTRING(hour, 1, 10) < latest.today) AS previous
		FROM latest
	), requested(metric, aggregation, ordinal) AS (
		VALUES
			('step_count', 'SUM', 1),
			('active_energy', 'SUM', 2),
			('basal_energy_burned', 'SUM', 3),
			('heart_rate', 'AVG', 4),
			('resting_heart_rate', 'AVG', 5),
			('heart_rate_variability', 'AVG', 6),
			('blood_oxygen_saturation', 'AVG', 7),
			('respiratory_rate', 'AVG', 8),
			('sleep_total', 'SUM', 9),
			('apple_exercise_time', 'SUM', 10),
			('walking_running_distance', 'SUM', 11),
			('wrist_temperature', 'AVG', 12)
	), source_totals AS (
		SELECT SUBSTRING(h.hour, 1, 10) AS date,
		       h.metric_name,
		       h.source,
		       SUM(h.avg_val)::double precision AS source_total
		FROM hourly_metrics h
		JOIN requested r ON r.metric = h.metric_name AND r.aggregation = 'SUM'
		CROSS JOIN days d
		WHERE SUBSTRING(h.hour, 1, 10) = d.today
		   OR SUBSTRING(h.hour, 1, 10) = d.previous
		GROUP BY SUBSTRING(h.hour, 1, 10), h.metric_name, h.source
	), sum_values AS (
		SELECT date,
		       metric_name,
		       CASE
		           WHEN metric_name LIKE 'sleep_%' THEN (` + sleepCrossValidationPickExpr("source_total") + `)
		           ELSE COALESCE(
		               MAX(CASE WHEN source LIKE '%Ultra%' OR source LIKE '%Apple Watch%' THEN source_total END),
		               MAX(CASE WHEN source LIKE '%iPhone%' THEN source_total END),
		               MAX(source_total)
		           )
		       END AS value
		FROM source_totals
		GROUP BY date, metric_name
	), avg_values AS (
		SELECT SUBSTRING(h.hour, 1, 10) AS date,
		       h.metric_name,
		       (SUM(h.avg_val * h.sample_count) /
		        NULLIF(SUM(h.sample_count), 0))::double precision AS value
		FROM hourly_metrics h
		JOIN requested r ON r.metric = h.metric_name AND r.aggregation = 'AVG'
		CROSS JOIN days d
		WHERE SUBSTRING(h.hour, 1, 10) = d.today
		   OR SUBSTRING(h.hour, 1, 10) = d.previous
		GROUP BY SUBSTRING(h.hour, 1, 10), h.metric_name
	), values_by_day AS (
		SELECT date, metric_name, value FROM sum_values
		UNION ALL
		SELECT date, metric_name, value FROM avg_values
	)
	SELECT d.today,
	       d.previous,
	       r.metric,
	       COALESCE(current.value, 0),
	       COALESCE(previous.value, 0)
	FROM days d
	CROSS JOIN requested r
	LEFT JOIN values_by_day current ON current.date = d.today AND current.metric_name = r.metric
	LEFT JOIN values_by_day previous ON previous.date = d.previous AND previous.metric_name = r.metric
	ORDER BY r.ordinal`

// LatestValue is the most recent value for a single metric, used by GetLatestMetricValues.
type LatestValue struct {
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Date   string  `json:"date"`
}

// GetLatestMetricValues returns the latest non-zero daily value for every metric in the DB.
// SUM metrics use MAX(per-source daily SUM) to avoid double-counting overlapping devices.
// AVG metrics use a simple daily AVG across all sources and hours.
// Reads from hourly_metrics (fast cache) instead of metric_points (4M+ rows).
func (s *DB) GetLatestMetricValues() ([]LatestValue, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	sumList := make([]string, 0, len(SumMetrics))
	for m := range SumMetrics {
		sumList = append(sumList, "'"+m+"'")
	}
	sumIn := strings.Join(sumList, ",")

	query := fmt.Sprintf(`
		WITH latest_day AS (
			SELECT metric_name, MAX(SUBSTRING(hour,1,10)) AS max_date
			FROM hourly_metrics
			GROUP BY metric_name
		),
		sum_agg AS (
			SELECT metric_name, max_date,
				CASE
					WHEN SUM(CASE WHEN source LIKE '%%|%%' THEN 1 ELSE 0 END) > 0
					THEN SUM(CASE WHEN source LIKE '%%|%%' THEN src_sum ELSE 0 END)
					ELSE MAX(src_sum)
				END AS val
			FROM (
				SELECT h.metric_name, l.max_date, h.source, SUM(h.avg_val) AS src_sum
				FROM hourly_metrics h
				JOIN latest_day l ON h.metric_name = l.metric_name
					AND SUBSTRING(h.hour,1,10) = l.max_date
				WHERE h.metric_name IN (%s)
				GROUP BY h.metric_name, l.max_date, h.source
			) sub GROUP BY metric_name, max_date
		),
		avg_agg AS (
			SELECT h.metric_name, l.max_date, AVG(h.avg_val) AS val
			FROM hourly_metrics h
			JOIN latest_day l ON h.metric_name = l.metric_name
				AND SUBSTRING(h.hour,1,10) = l.max_date
			WHERE h.metric_name NOT IN (%s)
			GROUP BY h.metric_name, l.max_date
		)
		SELECT metric_name, '', max_date, val FROM sum_agg
		UNION ALL
		SELECT metric_name, '', max_date, val FROM avg_agg
		ORDER BY metric_name
	`, sumIn, sumIn)

	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []LatestValue
	for rows.Next() {
		var v LatestValue
		if err := rows.Scan(&v.Metric, &v.Unit, &v.Date, &v.Value); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// MetricStats is returned by SummarizeMetric.
type MetricStats struct {
	Metric string      `json:"metric"`
	Units  string      `json:"units"`
	From   string      `json:"from"`
	To     string      `json:"to"`
	Count  int         `json:"count"`
	Avg    float64     `json:"avg"`
	Min    float64     `json:"min"`
	Max    float64     `json:"max"`
	Daily  []DataPoint `json:"daily"`
}

func (s *DB) ListMetrics() ([]MetricSummary, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	rows, err := s.pool.Query(ctx, `
		SELECT metric_name, '', COUNT(*) AS cnt, MIN(SUBSTRING(hour,1,10)), MAX(SUBSTRING(hour,1,10))
		FROM hourly_metrics
		GROUP BY metric_name
		ORDER BY cnt DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MetricSummary
	for rows.Next() {
		var m MetricSummary
		if err := rows.Scan(&m.Name, &m.Units, &m.Count, &m.Min, &m.Max); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *DB) GetMetricData(metric, from, to, bucket, aggFunc string) ([]DataPoint, error) {
	if aggFunc != "SUM" && aggFunc != "MAX" && aggFunc != "MIN" {
		aggFunc = "AVG"
	}

	switch bucket {
	case "minute":
		// Read directly from metric_points — minute_metrics is no longer populated.
		return s.metricDataRaw(metric, from, to, "minute", aggFuncFor(metric))
	case "hour":
		return s.metricDataFromCache("hourly_metrics", "hour", metric, from, to)
	case "day":
		return s.metricDataDayFromHourly(metric, from, to)
	}

	// Fallback: read directly from raw metric_points (should not be reached
	// in normal operation once the cache is populated).
	return s.metricDataRaw(metric, from, to, bucket, aggFunc)
}

// metricDataFromCache reads from a pre-aggregated table (minute_metrics or
// hourly_metrics), combining per-source rows using the metric's combine function.
func (s *DB) metricDataFromCache(table, col, metric, from, to string) ([]DataPoint, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	var combineVal string
	if SumMetrics[metric] {
		combineVal = sumCombineExpr("avg_val")
	} else {
		combineVal = "AVG(avg_val)"
	}
	query := fmt.Sprintf(`
		SELECT %s, %s, MIN(min_val), MAX(max_val)
		FROM %s
		WHERE metric_name = $1 AND %s >= $2 AND %s <= $3
		GROUP BY %s
		ORDER BY %s`, col, combineVal, table, col, col, col, col)

	rows, err := s.pool.Query(ctx, query, metric, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DataPoint
	for rows.Next() {
		var p DataPoint
		if err := rows.Scan(&p.Date, &p.Qty, &p.Min, &p.Max); err != nil {
			log.Printf("scan DataPoint: %v", err)
			continue
		}
		out = append(out, p)
	}

	// If cache is empty, fall back to raw data so the UI never returns nothing.
	if len(out) == 0 {
		bucket := "minute"
		if col == "hour" {
			bucket = "hour"
		}
		return s.metricDataRaw(metric, from, to, bucket, aggFuncFor(metric))
	}
	return out, rows.Err()
}

// metricDataDayFromHourly builds daily buckets by aggregating hourly_metrics.
// This is the third level of the cascade (hourly → daily).
// For any date range not covered by the hourly cache (e.g. historical Apple Health
// import data), it supplements with raw metric_points so the full history is visible.
func (s *DB) metricDataDayFromHourly(metric, from, to string) ([]DataPoint, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	// Find the earliest hour we have in the cache for this metric.
	var minHour *string
	s.pool.QueryRow(ctx, `SELECT MIN(hour) FROM hourly_metrics WHERE metric_name = $1`, metric).Scan(&minHour)

	var out []DataPoint

	// Determine the first cached date (day granularity).
	cacheStartDate := ""
	if minHour != nil && len(*minHour) >= 10 {
		cacheStartDate = (*minHour)[:10]
	}

	fromDate := from
	if len(fromDate) > 10 {
		fromDate = fromDate[:10]
	}

	// If there is historical data before the cache starts, read it directly from metric_points.
	if cacheStartDate == "" || fromDate < cacheStartDate {
		rawTo := to
		if cacheStartDate != "" {
			// Stop one day before the first cached day to avoid overlap.
			rawTo = cacheStartDate
		}
		rawPoints, rerr := s.metricDataRaw(metric, from, rawTo, "day", aggFuncFor(metric))
		if rerr == nil {
			out = append(out, rawPoints...)
		}
	}

	if cacheStartDate == "" {
		// No cache at all — raw data already returned above.
		return out, nil
	}

	// Read cached (hourly_metrics) portion.
	hourlyFrom := from
	if minHour != nil && *minHour > from {
		hourlyFrom = *minHour
	}

	var query string
	if SumMetrics[metric] {
		var pickExpr string
		if isSleepMetric(metric) {
			pickExpr = sleepCrossValidationPickExpr("source_total")
		} else {
			pickExpr = "MAX(source_total)"
		}
		query = fmt.Sprintf(`
			SELECT day, %s, MIN(source_min), MAX(source_max)
			FROM (
				SELECT SUBSTRING(hour,1,10) AS day, source,
				       SUM(avg_val) AS source_total, MIN(min_val) AS source_min, MAX(max_val) AS source_max
				FROM hourly_metrics
				WHERE metric_name = $1 AND hour >= $2 AND hour <= $3
				GROUP BY SUBSTRING(hour,1,10), source
			) sub
			GROUP BY day
			ORDER BY day`, pickExpr)
	} else {
		query = `
			SELECT SUBSTRING(hour,1,10), AVG(avg_val), MIN(min_val), MAX(max_val)
			FROM hourly_metrics
			WHERE metric_name = $1 AND hour >= $2 AND hour <= $3
			GROUP BY SUBSTRING(hour,1,10)
			ORDER BY SUBSTRING(hour,1,10)`
	}

	rows, err := s.pool.Query(ctx, query, metric, hourlyFrom, to)
	if err != nil {
		return out, err
	}
	defer rows.Close()

	for rows.Next() {
		var p DataPoint
		if err := rows.Scan(&p.Date, &p.Qty, &p.Min, &p.Max); err != nil {
			log.Printf("scan DataPoint: %v", err)
			continue
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// metricDataRaw reads directly from metric_points. Used as fallback when the
// pre-aggregated cache is empty, and for bucket=minute on short ranges before
// backfill runs.
func (s *DB) metricDataRaw(metric, from, to, bucket, aggFunc string) ([]DataPoint, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	bucketExpr := bucketExpression(bucket)
	if aggFunc != "SUM" && aggFunc != "MAX" && aggFunc != "MIN" {
		aggFunc = "AVG"
	}

	var query string
	if SumMetrics[metric] {
		sleepDedup := sleepDedupClause(metric)
		var pickExpr string
		if isSleepMetric(metric) {
			pickExpr = sleepCrossValidationPickExpr("source_sum")
		} else {
			pickExpr = sumCombineExpr("source_sum")
		}
		query = fmt.Sprintf(`SELECT bucket, %s, MIN(source_min), MAX(source_max)
			FROM (
				SELECT %s AS bucket, source, SUM(qty) AS source_sum, MIN(qty) AS source_min, MAX(qty) AS source_max
				FROM metric_points
				WHERE metric_name = $1 AND date >= $2 AND date <= $3 AND qty > 0 %s
				GROUP BY %s, source
			) sub
			GROUP BY bucket
			ORDER BY bucket`, pickExpr, bucketExpr, sleepDedup, bucketExpr)
	} else {
		query = "SELECT " + bucketExpr + " as bucket, " + aggFunc + `(qty), MIN(qty), MAX(qty)
			FROM metric_points
			WHERE metric_name = $1
			  AND date >= $2
			  AND date <= $3
			  AND qty > 0
			GROUP BY ` + bucketExpr + `
			ORDER BY ` + bucketExpr
	}

	rows, err := s.pool.Query(ctx, query, metric, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DataPoint
	for rows.Next() {
		var p DataPoint
		if err := rows.Scan(&p.Date, &p.Qty, &p.Min, &p.Max); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *DB) GetMetricDataBySource(metric, from, to, bucket, aggFunc string) ([]SourceDataPoints, error) {
	if aggFunc != "SUM" && aggFunc != "MAX" && aggFunc != "MIN" {
		aggFunc = "AVG"
	}

	pts, err := s.metricDataBySourceFromCache(metric, from, to, bucket)
	if err != nil || len(pts) == 0 {
		pts, err = s.metricDataBySourceRaw(metric, from, to, bucket, aggFunc)
	}
	return pts, err
}

func (s *DB) metricDataBySourceFromCache(metric, from, to, bucket string) ([]SourceDataPoints, error) {
	// minute_metrics is no longer populated; minute bucket falls through to raw.
	table, col := "hourly_metrics", "hour"
	if bucket == "minute" {
		return s.metricDataBySourceRaw(metric, from, to, bucket, aggFuncFor(metric))
	} else if bucket == "hour" {
		table, col = "hourly_metrics", "hour"
	} else if bucket == "day" {
		// Aggregate hourly_metrics down to day per source.
		table, col = "hourly_metrics", "hour"
		normSource := `SUBSTRING(source, 1, POSITION('|' IN source || '|') - 1)`
		agg := aggFuncFor(metric)
		query := fmt.Sprintf(`
			SELECT SUBSTRING(hour,1,10) as bkt, %s as src, %s(avg_val), MIN(min_val), MAX(max_val)
			FROM %s
			WHERE metric_name = $1 AND hour >= $2 AND hour <= $3
			GROUP BY SUBSTRING(hour,1,10), %s
			ORDER BY SUBSTRING(hour,1,10), %s`, normSource, agg, table, normSource, normSource)
		return s.scanSourcePoints(query, metric, from, to)
	}

	normSource := `SUBSTRING(source, 1, POSITION('|' IN source || '|') - 1)`
	agg := aggFuncFor(metric)
	query := fmt.Sprintf(`
		SELECT %s as bkt, %s as src, %s(avg_val), MIN(min_val), MAX(max_val)
		FROM %s
		WHERE metric_name = $1 AND %s >= $2 AND %s <= $3
		GROUP BY %s, %s
		ORDER BY %s, %s`, col, normSource, agg, table, col, col, col, normSource, col, normSource)
	return s.scanSourcePoints(query, metric, from, to)
}

func (s *DB) metricDataBySourceRaw(metric, from, to, bucket, aggFunc string) ([]SourceDataPoints, error) {
	bucketExpr := bucketExpression(bucket)
	normSource := `SUBSTRING(source, 1, POSITION('|' IN source || '|') - 1)`
	query := "SELECT " + bucketExpr + " as bucket, " + normSource + " as src, " + aggFunc + `(qty), MIN(qty), MAX(qty)
		FROM metric_points
		WHERE metric_name = $1 AND date >= $2 AND date <= $3 AND qty > 0
		` + sleepDedupClause(metric) + `
		GROUP BY ` + bucketExpr + `, ` + normSource + `
		ORDER BY ` + bucketExpr + `, ` + normSource
	return s.scanSourcePoints(query, metric, from, to)
}

func (s *DB) scanSourcePoints(query, metric, from, to string) ([]SourceDataPoints, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	rows, err := s.pool.Query(ctx, query, metric, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sourceMap := make(map[string][]DataPoint)
	var sourceOrder []string
	seen := make(map[string]bool)
	for rows.Next() {
		var bkt, src string
		var qty, mn, mx float64
		if err := rows.Scan(&bkt, &src, &qty, &mn, &mx); err != nil {
			return nil, err
		}
		if !seen[src] {
			seen[src] = true
			sourceOrder = append(sourceOrder, src)
		}
		sourceMap[src] = append(sourceMap[src], DataPoint{Date: bkt, Qty: qty, Min: mn, Max: mx})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var result []SourceDataPoints
	for _, src := range sourceOrder {
		result = append(result, SourceDataPoints{Source: src, Points: sourceMap[src]})
	}
	return result, nil
}

func (s *DB) GetDashboard() (*DashboardResponse, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	var payload []byte
	var completedAt time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT payload, completed_at
		FROM dashboard_cache_snapshots
		WHERE singleton = true`).Scan(&payload, &completedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &DashboardResponse{CacheState: dashboardCacheStateUnavailable}, nil
		}
		return nil, fmt.Errorf("read dashboard snapshot: %w", err)
	}

	var result DashboardResponse
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("decode dashboard snapshot: %w", err)
	}
	result.CacheCompletedAt = completedAt.UTC().Format(time.RFC3339Nano)
	if s.dashboardRefreshes.Load() > 0 {
		result.CacheState = dashboardCacheStateUpdating
	} else {
		result.CacheState = dashboardCacheStateComplete
	}
	return &result, nil
}

// refreshDashboardSnapshot is used by integration tests and migration tooling.
// Production cache writers call the locked form immediately after all cache
// tables for their generation have been updated successfully.
func (s *DB) refreshDashboardSnapshot(ctx context.Context) error {
	s.cacheMu.Lock()
	defer s.cacheMu.Unlock()
	return s.refreshDashboardSnapshotLocked(ctx)
}

func (s *DB) refreshDashboardSnapshotLocked(ctx context.Context) error {
	rows, err := s.pool.Query(ctx, dashboardCacheQuery)
	if err != nil {
		return fmt.Errorf("query dashboard cache: %w", err)
	}
	defer rows.Close()

	var result DashboardResponse
	for rows.Next() {
		var today, previous *string
		var metric string
		var value, previousValue float64
		if err := rows.Scan(&today, &previous, &metric, &value, &previousValue); err != nil {
			return fmt.Errorf("scan dashboard cache: %w", err)
		}
		if today == nil {
			break
		}
		result.Date = *today
		if value == 0 && (metric == "resting_heart_rate" || metric == "wrist_temperature") && previousValue != 0 {
			value = previousValue
		}
		if value == 0 {
			continue
		}
		result.Cards = append(result.Cards, CardData{
			Metric: metric,
			Value:  value,
			Prev:   previousValue,
			Date:   *today,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate dashboard cache: %w", err)
	}
	if result.Date != "" {
		units, err := s.dashboardSnapshotUnits(ctx, result.Date)
		if err != nil {
			return err
		}
		for i := range result.Cards {
			result.Cards[i].Unit = units[result.Cards[i].Metric]
		}
	}
	var lastUpdated *string
	if err := s.pool.QueryRow(ctx, `SELECT MAX(received_at) FROM health_records`).Scan(&lastUpdated); err != nil {
		return fmt.Errorf("read dashboard receipt timestamp: %w", err)
	}
	if lastUpdated != nil {
		result.LastUpdated = *lastUpdated
	}

	payload, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("encode dashboard snapshot: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO dashboard_cache_snapshots (singleton, completed_at, payload)
		VALUES (true, NOW(), $1)
		ON CONFLICT (singleton) DO UPDATE
		SET completed_at = EXCLUDED.completed_at, payload = EXCLUDED.payload`, json.RawMessage(payload)); err != nil {
		return fmt.Errorf("write dashboard snapshot: %w", err)
	}
	return nil
}

// dashboardSnapshotUnits retains a unit only when the current-day source
// data agrees on one non-empty value. A missing label is safer than claiming
// that miles are kilometres or kJ are kcal; this runs off-request-path while
// the completed snapshot is being built.
func (s *DB) dashboardSnapshotUnits(ctx context.Context, date string) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT metric_name,
			CASE
				WHEN COUNT(*) FILTER (WHERE NULLIF(BTRIM(units), '') IS NULL) = 0
				 AND COUNT(DISTINCT NULLIF(BTRIM(units), '')) = 1
				THEN MIN(NULLIF(BTRIM(units), ''))
				ELSE ''
			END AS unit
		FROM metric_points
		WHERE SUBSTRING(date, 1, 10) = $1
		GROUP BY metric_name`, date)
	if err != nil {
		return nil, fmt.Errorf("read dashboard units: %w", err)
	}
	defer rows.Close()
	units := make(map[string]string)
	for rows.Next() {
		var metric, unit string
		if err := rows.Scan(&metric, &unit); err != nil {
			return nil, fmt.Errorf("scan dashboard unit: %w", err)
		}
		units[metric] = unit
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dashboard units: %w", err)
	}
	return units, nil
}

func (s *DB) SummarizeMetric(metric string, days int) (*MetricStats, error) {
	if days <= 0 {
		days = 7
	}
	// Use latest date from data (not server time) to avoid timezone mismatch.
	_, maxDate, _ := s.GetMetricDateRange(metric)
	if maxDate == "" {
		return nil, fmt.Errorf("no data for %s", metric)
	}
	to := maxDate + " 23:59:59"
	t, err := time.Parse("2006-01-02", maxDate)
	if err != nil {
		return nil, fmt.Errorf("parse max date %s: %w", maxDate, err)
	}
	from := t.AddDate(0, 0, -(days - 1)).Format("2006-01-02")

	// Get daily-level data (already handles SUM/AVG and per-source dedup).
	daily, err := s.GetMetricData(metric, from, to, "day", aggFuncFor(metric))
	if err != nil || len(daily) == 0 {
		return nil, fmt.Errorf("no data for %s in last %d days", metric, days)
	}

	// Compute stats from daily values (correct for both SUM and AVG metrics).
	stats := MetricStats{
		Metric: metric,
		From:   daily[0].Date,
		To:     daily[len(daily)-1].Date,
		Count:  len(daily),
		Min:    daily[0].Qty,
		Max:    daily[0].Qty,
		Daily:  daily,
	}
	sum := 0.0
	for _, p := range daily {
		sum += p.Qty
		if p.Qty < stats.Min {
			stats.Min = p.Qty
		}
		if p.Qty > stats.Max {
			stats.Max = p.Qty
		}
	}
	stats.Avg = sum / float64(len(daily))

	// Look up units from metric_points.
	unitsCtx, unitsCancel := queryCtx()
	defer unitsCancel()
	var units *string
	s.pool.QueryRow(unitsCtx, `SELECT units FROM metric_points WHERE metric_name = $1 AND units != '' LIMIT 1`, metric).Scan(&units)
	if units != nil {
		stats.Units = *units
	}

	return &stats, nil
}

// SleepNight holds per-night sleep phase totals, deduplicated across devices.
type SleepNight struct {
	Date        string  `json:"date"`
	Total       float64 `json:"total"`
	Deep        float64 `json:"deep"`
	REM         float64 `json:"rem"`
	Core        float64 `json:"core"`
	Unspecified float64 `json:"unspecified,omitempty"`
	Awake       float64 `json:"awake"`
}

// GetSleepSummary returns per-night sleep breakdown for the date range.
// Uses preferredSleepSourceSQL (with cross-validation) per metric per night.
func (s *DB) GetSleepSummary(from, to string) ([]SleepNight, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	sleepDedup := sleepDedupClause("sleep_total")
	preferred := preferredSleepSourceSQL

	// Get all per-day, per-metric values using preferred source with cross-validation.
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT d, metric_name, (
			WITH source_totals AS (
				SELECT source, SUM(qty) AS source_total
				FROM metric_points
				WHERE metric_points.metric_name = sub.metric_name
				  AND SUBSTRING(metric_points.date,1,10) = sub.d
				  AND metric_points.qty > 0 %s
				GROUP BY source
			)
			%s
		) AS val
		FROM (
			SELECT DISTINCT SUBSTRING(date,1,10) AS d, metric_name
			FROM metric_points mp_outer
			WHERE mp_outer.metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_unspecified','sleep_awake')
			  AND SUBSTRING(mp_outer.date,1,10) >= $1 AND SUBSTRING(mp_outer.date,1,10) <= $2 AND mp_outer.qty > 0
		) sub
		ORDER BY d`, sleepDedup, preferred),
		from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nights := map[string]*SleepNight{}
	for rows.Next() {
		var d, metric string
		var val float64
		if err := rows.Scan(&d, &metric, &val); err != nil {
			return nil, err
		}
		n, ok := nights[d]
		if !ok {
			n = &SleepNight{Date: d}
			nights[d] = n
		}
		switch metric {
		case "sleep_total":
			n.Total = val
		case "sleep_deep":
			n.Deep = val
		case "sleep_rem":
			n.REM = val
		case "sleep_core":
			n.Core = val
		case "sleep_unspecified":
			n.Unspecified = val
		case "sleep_awake":
			n.Awake = val
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Sort by date.
	dates := make([]string, 0, len(nights))
	for d := range nights {
		dates = append(dates, d)
	}
	sort.Strings(dates)
	out := make([]SleepNight, 0, len(dates))
	for _, d := range dates {
		out = append(out, *nights[d])
	}
	return out, nil
}

// QueryReadOnly executes an arbitrary SELECT and returns results as []map[string]any.
func (s *DB) QueryReadOnly(query string, args ...any) ([]map[string]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	q := strings.TrimSpace(strings.ToUpper(query))
	if !strings.HasPrefix(q, "SELECT") {
		return nil, fmt.Errorf("only SELECT queries are allowed")
	}
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	fields := rows.FieldDescriptions()
	cols := make([]string, len(fields))
	for i, fd := range fields {
		cols[i] = string(fd.Name)
	}

	var result []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			row[c] = vals[i]
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// bucketExpression returns the SQL expression that truncates a date column
// to the requested time bucket.
func bucketExpression(bucket string) string {
	switch bucket {
	case "hour":
		return "SUBSTRING(date, 1, 13) || ':00'"
	case "day":
		return "SUBSTRING(date, 1, 10)"
	default: // minute
		return "SUBSTRING(date, 1, 16)"
	}
}

// GetMetricDateRange returns the earliest and latest dates for a metric.
// Returns empty strings (no error) when the metric has no data.
func (s *DB) GetMetricDateRange(metric string) (min, max string, err error) {
	ctx, cancel := queryCtx()
	defer cancel()

	var minN, maxN *string
	err = s.pool.QueryRow(ctx,
		`SELECT SUBSTRING(MIN(date),1,10), SUBSTRING(MAX(date),1,10) FROM metric_points WHERE metric_name = $1`,
		metric,
	).Scan(&minN, &maxN)
	if err == nil {
		if minN != nil {
			min = *minN
		}
		if maxN != nil {
			max = *maxN
		}
	}
	return
}

// GetLatestMetricDate returns the latest date across all metric_points as a Unix timestamp.
// Returns 0 (no error) when the table is empty.
func (s *DB) GetLatestMetricDate() (int64, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	var ts *int64
	err := s.pool.QueryRow(ctx,
		`SELECT EXTRACT(EPOCH FROM MAX(date::TIMESTAMPTZ))::BIGINT FROM metric_points`,
	).Scan(&ts)
	if err != nil {
		return 0, err
	}
	if ts == nil {
		return 0, nil
	}
	return *ts, nil
}
