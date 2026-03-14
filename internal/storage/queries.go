package storage

import (
	"database/sql"
	"fmt"
	"strings"
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
	Date        string     `json:"date"`
	LastUpdated string     `json:"last_updated"`
	Cards       []CardData `json:"cards"`
}

// LatestValue is the most recent value for a single metric, used by GetLatestMetricValues.
type LatestValue struct {
	Metric string  `json:"metric"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
	Date   string  `json:"date"`
}

// GetLatestMetricValues returns the latest non-zero daily average for every metric in the DB.
// Reads from hourly_metrics (fast cache, ~100K rows) instead of metric_points (4M+ rows).
func (s *DB) GetLatestMetricValues() ([]LatestValue, error) {
	rows, err := s.db.Query(`
		WITH latest_day AS (
			SELECT metric_name, MAX(substr(hour,1,10)) AS max_date
			FROM hourly_metrics
			GROUP BY metric_name
		)
		SELECT h.metric_name, '', l.max_date, AVG(h.avg_val)
		FROM hourly_metrics h
		JOIN latest_day l ON h.metric_name = l.metric_name
			AND substr(h.hour,1,10) = l.max_date
		GROUP BY h.metric_name
		ORDER BY h.metric_name
	`)
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
	rows, err := s.db.Query(`
		SELECT metric_name, '', COUNT(*) AS cnt, MIN(substr(hour,1,10)), MAX(substr(hour,1,10))
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
		return s.metricDataFromCache("minute_metrics", "minute", metric, from, to)
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
	combine := combineFuncFor(metric)
	query := fmt.Sprintf(`
		SELECT %s, %s(avg_val), MIN(min_val), MAX(max_val)
		FROM %s
		WHERE metric_name = ? AND %s >= ? AND %s <= ?
		GROUP BY %s
		ORDER BY %s`, col, combine, table, col, col, col, col)

	rows, err := s.db.Query(query, metric, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DataPoint
	for rows.Next() {
		var p DataPoint
		if rows.Scan(&p.Date, &p.Qty, &p.Min, &p.Max) == nil {
			out = append(out, p)
		}
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
	// Find the earliest hour we have in the cache for this metric.
	var minHour string
	s.db.QueryRow(`SELECT MIN(hour) FROM hourly_metrics WHERE metric_name = ?`, metric).Scan(&minHour)

	var out []DataPoint

	// If there is historical data before the cache starts, read it directly from metric_points.
	if minHour == "" || from < minHour {
		rawTo := to
		if minHour != "" && minHour[:10] > from[:10] {
			// Stop just before the first cached day to avoid overlap.
			rawTo = minHour[:10] + " 00:00:00"
		}
		rawPoints, rerr := s.metricDataRaw(metric, from, rawTo, "day", aggFuncFor(metric))
		if rerr == nil {
			out = append(out, rawPoints...)
		}
	}

	if minHour == "" {
		// No cache at all — raw data already returned above.
		return out, nil
	}

	// Read cached (hourly_metrics) portion.
	hourlyFrom := from
	if minHour > from {
		hourlyFrom = minHour
	}
	combine := combineFuncFor(metric)
	query := fmt.Sprintf(`
		SELECT substr(hour,1,10), %s(avg_val), MIN(min_val), MAX(max_val)
		FROM hourly_metrics
		WHERE metric_name = ? AND hour >= ? AND hour <= ?
		GROUP BY substr(hour,1,10)
		ORDER BY substr(hour,1,10)`, combine)

	rows, err := s.db.Query(query, metric, hourlyFrom, to)
	if err != nil {
		return out, err
	}
	defer rows.Close()

	for rows.Next() {
		var p DataPoint
		if rows.Scan(&p.Date, &p.Qty, &p.Min, &p.Max) == nil {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// metricDataRaw reads directly from metric_points. Used as fallback when the
// pre-aggregated cache is empty, and for bucket=minute on short ranges before
// backfill runs.
func (s *DB) metricDataRaw(metric, from, to, bucket, aggFunc string) ([]DataPoint, error) {
	bucketExpr := bucketExpression(bucket)
	if aggFunc != "SUM" && aggFunc != "MAX" && aggFunc != "MIN" {
		aggFunc = "AVG"
	}

	var query string
	if aggFunc == "SUM" && strings.HasPrefix(metric, "sleep_") {
		query = `SELECT bucket, MAX(source_sum), MIN(source_min), MAX(source_max)
			FROM (
				SELECT ` + bucketExpr + ` AS bucket, source, SUM(qty) AS source_sum, MIN(qty) AS source_min, MAX(qty) AS source_max
				FROM metric_points
				WHERE metric_name = ? AND date >= ? AND date <= ? AND qty > 0
				GROUP BY bucket, source
			)
			GROUP BY bucket
			ORDER BY bucket`
	} else {
		query = "SELECT " + bucketExpr + " as bucket, " + aggFunc + `(qty), MIN(qty), MAX(qty)
			FROM metric_points
			WHERE metric_name = ?
			  AND date >= ?
			  AND date <= ?
			  AND qty > 0
			GROUP BY bucket
			ORDER BY bucket`
	}

	rows, err := s.db.Query(query, metric, from, to)
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
	table, col := "minute_metrics", "minute"
	if bucket == "hour" {
		table, col = "hourly_metrics", "hour"
	} else if bucket == "day" {
		// Aggregate hourly_metrics down to day per source.
		table, col = "hourly_metrics", "hour"
		normSource := `SUBSTR(source, 1, INSTR(source || '|', '|') - 1)`
		agg := aggFuncFor(metric)
		query := fmt.Sprintf(`
			SELECT substr(hour,1,10) as bkt, %s as src, %s(avg_val), MIN(min_val), MAX(max_val)
			FROM %s
			WHERE metric_name = ? AND hour >= ? AND hour <= ?
			GROUP BY bkt, src
			ORDER BY bkt, src`, normSource, agg, table)
		return s.scanSourcePoints(query, metric, from, to)
	}

	normSource := `SUBSTR(source, 1, INSTR(source || '|', '|') - 1)`
	agg := aggFuncFor(metric)
	query := fmt.Sprintf(`
		SELECT %s as bkt, %s as src, %s(avg_val), MIN(min_val), MAX(max_val)
		FROM %s
		WHERE metric_name = ? AND %s >= ? AND %s <= ?
		GROUP BY bkt, src
		ORDER BY bkt, src`, col, normSource, agg, table, col, col)
	return s.scanSourcePoints(query, metric, from, to)
}

func (s *DB) metricDataBySourceRaw(metric, from, to, bucket, aggFunc string) ([]SourceDataPoints, error) {
	bucketExpr := bucketExpression(bucket)
	normSource := `SUBSTR(source, 1, INSTR(source || '|', '|') - 1)`
	query := "SELECT " + bucketExpr + " as bucket, " + normSource + " as src, " + aggFunc + `(qty), MIN(qty), MAX(qty)
		FROM metric_points
		WHERE metric_name = ? AND date >= ? AND date <= ? AND qty > 0
		GROUP BY bucket, src
		ORDER BY bucket, src`
	return s.scanSourcePoints(query, metric, from, to)
}

func (s *DB) scanSourcePoints(query, metric, from, to string) ([]SourceDataPoints, error) {
	rows, err := s.db.Query(query, metric, from, to)
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
	var today string
	if err := s.db.QueryRow(`SELECT MAX(substr(hour,1,10)) FROM hourly_metrics`).Scan(&today); err != nil || today == "" {
		return &DashboardResponse{}, nil
	}

	var yesterday string
	s.db.QueryRow(`SELECT MAX(substr(hour,1,10)) FROM hourly_metrics WHERE substr(hour,1,10) < ?`, today).Scan(&yesterday)

	var lastUpdated string
	s.db.QueryRow(`SELECT MAX(received_at) FROM health_records`).Scan(&lastUpdated)

	type spec struct {
		metric string
		agg    string
	}
	cards := []spec{
		{"step_count", "SUM"},
		{"active_energy", "SUM"},
		{"basal_energy_burned", "SUM"},
		{"heart_rate", "AVG"},
		{"resting_heart_rate", "AVG"},
		{"heart_rate_variability_sdnn", "AVG"},
		{"oxygen_saturation", "AVG"},
		{"respiratory_rate", "AVG"},
		{"sleep_total", "SUM"},
		{"apple_exercise_time", "SUM"},
		{"walking_running_distance", "SUM"},
		{"wrist_temperature", "AVG"},
	}

	queryDay := func(metric, agg, day string) float64 {
		var val float64
		// Sleep metrics need MAX-per-source to avoid double-counting from two devices.
		if agg == "SUM" && strings.HasPrefix(metric, "sleep_") {
			s.db.QueryRow(`
				SELECT COALESCE(MAX(source_sum), 0) FROM (
					SELECT source, SUM(sum_val) AS source_sum
					FROM hourly_metrics
					WHERE metric_name=? AND substr(hour,1,10)=?
					GROUP BY source
				)`, metric, day,
			).Scan(&val)
		} else if agg == "SUM" {
			s.db.QueryRow(
				`SELECT COALESCE(SUM(sum_val), 0) FROM hourly_metrics WHERE metric_name=? AND substr(hour,1,10)=?`,
				metric, day,
			).Scan(&val)
		} else {
			s.db.QueryRow(
				`SELECT COALESCE(AVG(avg_val), 0) FROM hourly_metrics WHERE metric_name=? AND substr(hour,1,10)=?`,
				metric, day,
			).Scan(&val)
		}
		return val
	}

	// Look up units from hourly_metrics (stored there) falling back to metric_points.
	unitFor := func(metric string) string {
		var u string
		s.db.QueryRow(`SELECT units FROM hourly_metrics WHERE metric_name=? LIMIT 1`, metric).Scan(&u)
		return u
	}

	var result []CardData
	for _, c := range cards {
		val := queryDay(c.metric, c.agg, today)
		if val == 0 {
			continue
		}
		prev := queryDay(c.metric, c.agg, yesterday)
		result = append(result, CardData{
			Metric: c.metric, Value: val, Prev: prev,
			Unit: unitFor(c.metric), Date: today,
		})
	}
	return &DashboardResponse{Date: today, LastUpdated: lastUpdated, Cards: result}, nil
}

func (s *DB) SummarizeMetric(metric string, days int) (*MetricStats, error) {
	if days <= 0 {
		days = 7
	}
	from := fmt.Sprintf("date('now','-%d days')", days-1)

	var stats MetricStats
	var units string
	err := s.db.QueryRow(`
		SELECT metric_name, units, COUNT(*), AVG(qty), MIN(qty), MAX(qty),
		       MIN(date), MAX(date)
		FROM metric_points
		WHERE metric_name = ? AND date >= `+from+` AND qty > 0`,
		metric,
	).Scan(&stats.Metric, &units, &stats.Count, &stats.Avg, &stats.Min, &stats.Max, &stats.From, &stats.To)
	if err != nil {
		return nil, err
	}
	stats.Units = units

	daily, err := s.GetMetricData(metric, stats.From[:10], stats.To[:10]+" 23:59:59", "day", "AVG")
	if err == nil {
		stats.Daily = daily
	}
	return &stats, nil
}

// SleepNight holds per-night sleep phase totals, deduplicated across devices.
type SleepNight struct {
	Date  string  `json:"date"`
	Total float64 `json:"total"`
	Deep  float64 `json:"deep"`
	REM   float64 `json:"rem"`
	Core  float64 `json:"core"`
	Awake float64 `json:"awake"`
}

// GetSleepSummary returns per-night sleep breakdown for the date range.
// For each phase it picks MAX(source_sum) across devices to avoid double-counting
// when two wearables (e.g. Apple Watch + RingConn) both record the same night.
func (s *DB) GetSleepSummary(from, to string) ([]SleepNight, error) {
	rows, err := s.db.Query(`
		SELECT d,
			MAX(CASE WHEN metric_name='sleep_total' THEN source_max ELSE 0 END),
			MAX(CASE WHEN metric_name='sleep_deep'  THEN source_max ELSE 0 END),
			MAX(CASE WHEN metric_name='sleep_rem'   THEN source_max ELSE 0 END),
			MAX(CASE WHEN metric_name='sleep_core'  THEN source_max ELSE 0 END),
			MAX(CASE WHEN metric_name='sleep_awake' THEN source_max ELSE 0 END)
		FROM (
			SELECT d, metric_name, MAX(source_sum) AS source_max
			FROM (
				SELECT substr(date,1,10) AS d, metric_name,
					SUBSTR(source, 1, INSTR(source||'|','|')-1) AS src,
					SUM(qty) AS source_sum
				FROM metric_points
				WHERE metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake')
				  AND substr(date,1,10) >= ? AND substr(date,1,10) <= ? AND qty > 0
				GROUP BY d, metric_name, src
			)
			GROUP BY d, metric_name
		)
		GROUP BY d
		ORDER BY d`,
		from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SleepNight
	for rows.Next() {
		var n SleepNight
		if err := rows.Scan(&n.Date, &n.Total, &n.Deep, &n.REM, &n.Core, &n.Awake); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// QueryReadOnly executes an arbitrary SELECT and returns results as []map[string]any.
func (s *DB) QueryReadOnly(query string) ([]map[string]any, error) {
	q := strings.TrimSpace(strings.ToUpper(query))
	if !strings.HasPrefix(q, "SELECT") {
		return nil, fmt.Errorf("only SELECT queries are allowed")
	}
	rows, err := s.db.Query(query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
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
		return "substr(date, 1, 13) || ':00'"
	case "day":
		return "substr(date, 1, 10)"
	default: // minute
		return "substr(date, 1, 16)"
	}
}

// GetMetricDateRange returns the earliest and latest dates for a metric.
// Returns empty strings (no error) when the metric has no data.
func (s *DB) GetMetricDateRange(metric string) (min, max string, err error) {
	var minN, maxN sql.NullString
	err = s.db.QueryRow(
		`SELECT substr(MIN(date),1,10), substr(MAX(date),1,10) FROM metric_points WHERE metric_name = ?`,
		metric,
	).Scan(&minN, &maxN)
	if err == nil {
		min = minN.String
		max = maxN.String
	}
	return
}
