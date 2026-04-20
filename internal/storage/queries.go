package storage

import (
	"fmt"
	"log"
	"sort"
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
			// Sleep: Apple Watch > RingConn + cross-validation (>40% divergence → take MIN).
			pickExpr = `CASE
				WHEN COUNT(*) > 1 AND MAX(source_total) > MIN(source_total) * 1.4
				THEN MIN(source_total)
				ELSE COALESCE(
				    MAX(CASE WHEN source LIKE '%Ultra%' OR source LIKE '%Apple Watch%' THEN source_total END),
				    MAX(CASE WHEN source LIKE '%RingConn%' THEN source_total END),
				    MAX(source_total)
				) END`
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
			pickExpr = `CASE
				WHEN COUNT(*) > 1 AND MAX(source_sum) > MIN(source_sum) * 1.4
				THEN MIN(source_sum)
				ELSE COALESCE(
				    MAX(CASE WHEN source LIKE '%Ultra%' OR source LIKE '%Apple Watch%' THEN source_sum END),
				    MAX(CASE WHEN source LIKE '%RingConn%' THEN source_sum END),
				    MAX(source_sum)
				) END`
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

	// Detect "today" from the descending index on SUBSTRING(date,1,10) — fast index-only scan.
	var today *string
	if err := s.pool.QueryRow(ctx,
		`SELECT SUBSTRING(date,1,10) FROM metric_points ORDER BY SUBSTRING(date,1,10) DESC LIMIT 1`,
	).Scan(&today); err != nil || today == nil {
		return &DashboardResponse{}, nil
	}

	// "yesterday" from hourly_metrics (smaller table, already cached).
	var yesterday *string
	s.pool.QueryRow(ctx,
		`SELECT MAX(SUBSTRING(hour,1,10)) FROM hourly_metrics WHERE SUBSTRING(hour,1,10) < $1`, *today,
	).Scan(&yesterday)

	var lastUpdated *string
	s.pool.QueryRow(ctx, `SELECT MAX(received_at) FROM health_records`).Scan(&lastUpdated)

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

	queryDayRaw := func(metric, agg, day string) float64 {
		var val float64
		if agg == "SUM" {
			sleepDedup := sleepDedupClause(metric)
			query := fmt.Sprintf(`
				WITH source_totals AS (
					SELECT source, SUM(qty) AS source_total
					FROM metric_points
					WHERE metric_name=$1 AND SUBSTRING(date,1,10)=$2 AND qty > 0 %s
					GROUP BY source
				) `, sleepDedup) + preferredSourceForMetric(metric)
			s.pool.QueryRow(ctx, query, metric, day).Scan(&val)
		} else {
			s.pool.QueryRow(ctx,
				`SELECT COALESCE(AVG(qty), 0) FROM metric_points WHERE metric_name=$1 AND SUBSTRING(date,1,10)=$2 AND qty > 0`,
				metric, day,
			).Scan(&val)
		}
		return val
	}

	queryDayCache := func(metric, agg, day string) float64 {
		var val float64
		if agg == "SUM" {
			s.pool.QueryRow(ctx, `
				WITH source_totals AS (
					SELECT source, SUM(avg_val) AS source_total
					FROM hourly_metrics
					WHERE metric_name=$1 AND SUBSTRING(hour,1,10)=$2
					GROUP BY source
				) `+preferredSourceForMetric(metric), metric, day,
			).Scan(&val)
		} else {
			s.pool.QueryRow(ctx,
				`SELECT COALESCE(AVG(avg_val), 0) FROM hourly_metrics WHERE metric_name=$1 AND SUBSTRING(hour,1,10)=$2`,
				metric, day,
			).Scan(&val)
		}
		return val
	}

	// Batch units lookup (1 query instead of 12)
	unitMap := make(map[string]string)
	unitRows, err := s.pool.Query(ctx, `
		SELECT metric_name, units
		FROM metric_points
		WHERE metric_name IN ('step_count','active_energy','basal_energy_burned',
		      'heart_rate','resting_heart_rate','heart_rate_variability',
		      'blood_oxygen_saturation','respiratory_rate','sleep_total',
		      'apple_exercise_time','walking_running_distance','wrist_temperature')
		  AND units IS NOT NULL AND units != ''
		GROUP BY metric_name, units`)
	if err == nil {
		defer unitRows.Close()
		for unitRows.Next() {
			var name, unit string
			if err := unitRows.Scan(&name, &unit); err == nil {
				unitMap[name] = unit
			}
		}
	}

	yesterdayStr := ""
	if yesterday != nil {
		yesterdayStr = *yesterday
	}
	lastUpdatedStr := ""
	if lastUpdated != nil {
		lastUpdatedStr = *lastUpdated
	}

	// Metrics that are reported infrequently (once per day, often late).
	// When today's value is missing, fall back to yesterday's so the card is not dropped.
	slowMetrics := map[string]bool{
		"resting_heart_rate": true,
		"wrist_temperature":  true,
	}

	var result []CardData
	for _, c := range cards {
		val := queryDayRaw(c.metric, c.agg, *today)
		prev := queryDayCache(c.metric, c.agg, yesterdayStr)
		if val == 0 {
			if slowMetrics[c.metric] && prev != 0 {
				val = prev
			} else {
				continue
			}
		}
		result = append(result, CardData{
			Metric: c.metric, Value: val, Prev: prev,
			Unit: unitMap[c.metric], Date: *today,
		})
	}
	return &DashboardResponse{Date: *today, LastUpdated: lastUpdatedStr, Cards: result}, nil
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
	Date  string  `json:"date"`
	Total float64 `json:"total"`
	Deep  float64 `json:"deep"`
	REM   float64 `json:"rem"`
	Core  float64 `json:"core"`
	Awake float64 `json:"awake"`
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
			WHERE mp_outer.metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake')
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
func (s *DB) QueryReadOnly(query string) ([]map[string]any, error) {
	ctx, cancel := queryCtx()
	defer cancel()

	q := strings.TrimSpace(strings.ToUpper(query))
	if !strings.HasPrefix(q, "SELECT") {
		return nil, fmt.Errorf("only SELECT queries are allowed")
	}
	rows, err := s.pool.Query(ctx, query)
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
