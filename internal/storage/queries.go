package storage

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
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

// GetLatestMetricValues returns the latest non-zero daily value for every metric in the DB.
// SUM metrics use MAX(per-source daily SUM) to avoid double-counting overlapping devices.
// AVG metrics use a simple daily AVG across all sources and hours.
// Reads from hourly_metrics (fast cache) instead of metric_points (4M+ rows).
func (s *DB) GetLatestMetricValues() ([]LatestValue, error) {
	sumList := make([]string, 0, len(SumMetrics))
	for m := range SumMetrics {
		sumList = append(sumList, "'"+m+"'")
	}
	sumIn := strings.Join(sumList, ",")

	query := fmt.Sprintf(`
		WITH latest_day AS (
			SELECT metric_name, MAX(substr(hour,1,10)) AS max_date
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
					AND substr(h.hour,1,10) = l.max_date
				WHERE h.metric_name IN (%s)
				GROUP BY h.metric_name, h.source
			) GROUP BY metric_name
		),
		avg_agg AS (
			SELECT h.metric_name, l.max_date, AVG(h.avg_val) AS val
			FROM hourly_metrics h
			JOIN latest_day l ON h.metric_name = l.metric_name
				AND substr(h.hour,1,10) = l.max_date
			WHERE h.metric_name NOT IN (%s)
			GROUP BY h.metric_name
		)
		SELECT metric_name, '', max_date, val FROM sum_agg
		UNION ALL
		SELECT metric_name, '', max_date, val FROM avg_agg
		ORDER BY metric_name
	`, sumIn, sumIn)

	rows, err := s.db.Query(query)
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
	var combineVal string
	if SumMetrics[metric] {
		combineVal = sumCombineExpr("avg_val")
	} else {
		combineVal = "AVG(avg_val)"
	}
	query := fmt.Sprintf(`
		SELECT %s, %s, MIN(min_val), MAX(max_val)
		FROM %s
		WHERE metric_name = ? AND %s >= ? AND %s <= ?
		GROUP BY %s
		ORDER BY %s`, col, combineVal, table, col, col, col, col)

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

	// Determine the first cached date (day granularity).
	cacheStartDate := ""
	if minHour != "" && len(minHour) >= 10 {
		cacheStartDate = minHour[:10]
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
	if minHour > from {
		hourlyFrom = minHour
	}

	var query string
	if SumMetrics[metric] {
		// SUM metrics: smart combine per hour, then SUM across hours per day.
		combineVal := sumCombineExpr("avg_val")
		query = fmt.Sprintf(`
			SELECT day, SUM(hour_val), MIN(hour_min), MAX(hour_max)
			FROM (
				SELECT substr(hour,1,10) AS day, hour,
				       %s AS hour_val, MIN(min_val) AS hour_min, MAX(max_val) AS hour_max
				FROM hourly_metrics
				WHERE metric_name = ? AND hour >= ? AND hour <= ?
				GROUP BY hour
			)
			GROUP BY day
			ORDER BY day`, combineVal)
	} else {
		query = `
			SELECT substr(hour,1,10), AVG(avg_val), MIN(min_val), MAX(max_val)
			FROM hourly_metrics
			WHERE metric_name = ? AND hour >= ? AND hour <= ?
			GROUP BY substr(hour,1,10)
			ORDER BY substr(hour,1,10)`
	}

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
	if SumMetrics[metric] {
		// SUM metrics: smart combine — prefer pipe-source (HealthKit deduped),
		// fall back to MAX per single-source (XML import overlap).
		combineVal := sumCombineExpr("source_sum")
		query = fmt.Sprintf(`SELECT bucket, %s, MIN(source_min), MAX(source_max)
			FROM (
				SELECT %s AS bucket, source, SUM(qty) AS source_sum, MIN(qty) AS source_min, MAX(qty) AS source_max
				FROM metric_points
				WHERE metric_name = ? AND date >= ? AND date <= ? AND qty > 0
				GROUP BY bucket, source
			)
			GROUP BY bucket
			ORDER BY bucket`, combineVal, bucketExpr)
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
	// Use metric_points for "today" detection — always fresh after POST.
	var today string
	if err := s.db.QueryRow(`SELECT MAX(substr(date,1,10)) FROM metric_points`).Scan(&today); err != nil || today == "" {
		return &DashboardResponse{}, nil
	}

	var yesterday string
	s.db.QueryRow(`SELECT MAX(substr(date,1,10)) FROM metric_points WHERE substr(date,1,10) < ?`, today).Scan(&yesterday)

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
		{"heart_rate_variability", "AVG"},
		{"blood_oxygen_saturation", "AVG"},
		{"respiratory_rate", "AVG"},
		{"sleep_total", "SUM"},
		{"apple_exercise_time", "SUM"},
		{"walking_running_distance", "SUM"},
		{"wrist_temperature", "AVG"},
	}

	// queryDayRaw reads a metric's daily value directly from metric_points
	// (always fresh). Used for "today" to avoid stale cache.
	queryDayRaw := func(metric, agg, day string) float64 {
		var val float64
		if agg == "SUM" {
			// Sleep dedup: exclude midnight summary when fragments exist.
			sleepDedup := ""
			if strings.HasPrefix(metric, "sleep_") {
				sleepDedup = `AND NOT (
					substr(date, 12, 8) = '00:00:00'
					AND EXISTS (
						SELECT 1 FROM metric_points p2
						WHERE p2.metric_name = metric_points.metric_name
						  AND substr(p2.date, 1, 10) = substr(metric_points.date, 1, 10)
						  AND p2.source = metric_points.source
						  AND substr(p2.date, 12, 8) != '00:00:00'
						  AND p2.qty > 0
					)
				)`
			}
			combineVal := sumCombineExpr("source_sum")
			s.db.QueryRow(fmt.Sprintf(`
				SELECT COALESCE(%s, 0) FROM (
					SELECT source, SUM(qty) AS source_sum
					FROM metric_points
					WHERE metric_name=? AND substr(date,1,10)=? AND qty > 0 %s
					GROUP BY source
				)`, combineVal, sleepDedup), metric, day,
			).Scan(&val)
		} else {
			s.db.QueryRow(
				`SELECT COALESCE(AVG(qty), 0) FROM metric_points WHERE metric_name=? AND substr(date,1,10)=? AND qty > 0`,
				metric, day,
			).Scan(&val)
		}
		return val
	}

	// queryDayCache reads from hourly_metrics (fast, for historical days).
	queryDayCache := func(metric, agg, day string) float64 {
		var val float64
		if agg == "SUM" {
			combineVal := sumCombineExpr("avg_val")
			s.db.QueryRow(fmt.Sprintf(`
				SELECT COALESCE(SUM(hour_val), 0) FROM (
					SELECT hour, %s AS hour_val
					FROM hourly_metrics
					WHERE metric_name=? AND substr(hour,1,10)=?
					GROUP BY hour
				)`, combineVal), metric, day,
			).Scan(&val)
		} else {
			s.db.QueryRow(
				`SELECT COALESCE(AVG(avg_val), 0) FROM hourly_metrics WHERE metric_name=? AND substr(hour,1,10)=?`,
				metric, day,
			).Scan(&val)
		}
		return val
	}

	// Look up units from metric_points (hourly_metrics has no units column).
	unitFor := func(metric string) string {
		var u string
		s.db.QueryRow(`SELECT units FROM metric_points WHERE metric_name=? AND units != '' LIMIT 1`, metric).Scan(&u)
		return u
	}

	var result []CardData
	for _, c := range cards {
		// Today: read from metric_points (always fresh after POST).
		// Yesterday: read from hourly_metrics cache (fast, stable).
		val := queryDayRaw(c.metric, c.agg, today)
		if val == 0 {
			continue
		}
		prev := queryDayCache(c.metric, c.agg, yesterday)
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
	s.db.QueryRow(`SELECT units FROM metric_points WHERE metric_name = ? AND units != '' LIMIT 1`, metric).Scan(&stats.Units)

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
	combine := sumCombineExpr("source_sum")
	rows, err := s.db.Query(fmt.Sprintf(`
		SELECT d,
			MAX(CASE WHEN metric_name='sleep_total' THEN combined ELSE 0 END),
			MAX(CASE WHEN metric_name='sleep_deep'  THEN combined ELSE 0 END),
			MAX(CASE WHEN metric_name='sleep_rem'   THEN combined ELSE 0 END),
			MAX(CASE WHEN metric_name='sleep_core'  THEN combined ELSE 0 END),
			MAX(CASE WHEN metric_name='sleep_awake' THEN combined ELSE 0 END)
		FROM (
			SELECT d, metric_name, %s AS combined
			FROM (
				SELECT substr(date,1,10) AS d, metric_name, source,
					SUM(qty) AS source_sum
				FROM metric_points
				WHERE metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake')
				  AND substr(date,1,10) >= ? AND substr(date,1,10) <= ? AND qty > 0
				GROUP BY d, metric_name, source
			)
			GROUP BY d, metric_name
		)
		GROUP BY d
		ORDER BY d`, combine),
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
