package storage

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"health-receiver/internal/health"
)

// rawMetricsFromDailyScores reads 30 days of pre-aggregated metrics from the
// daily_scores cache in a single query. Returns nil if the cache is empty or
// has no usable rows (cold start).
func (s *DB) rawMetricsFromDailyScores(lastDate string) *health.RawMetrics {
	ctx := context.Background()
	rows, err := s.pool.Query(ctx, `
		SELECT date, hrv_avg, rhr_avg, sleep_total, sleep_deep, sleep_rem,
		       sleep_core, sleep_awake, steps, calories, exercise_min,
		       spo2_avg, vo2_avg, resp_avg
		FROM daily_scores
		WHERE date >= $1 AND date <= $2
		  AND (hrv_avg IS NOT NULL OR sleep_total IS NOT NULL OR steps IS NOT NULL)
		ORDER BY date DESC
		LIMIT 30`,
		subtractDays(lastDate, 29), lastDate)
	if err != nil {
		return nil
	}
	defer rows.Close()

	type row struct {
		date                                         string
		hrv, rhr, slp, deep, rem, core, awake        *float64
		steps, cal, ex, spo2, vo2, resp              *float64
	}

	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(
			&r.date, &r.hrv, &r.rhr, &r.slp, &r.deep, &r.rem,
			&r.core, &r.awake, &r.steps, &r.cal, &r.ex,
			&r.spo2, &r.vo2, &r.resp,
		); err == nil {
			all = append(all, r)
		}
	}
	if len(all) == 0 {
		return nil
	}

	// appendIfPositive only appends real positive values — NULL or zero days
	// are skipped so they don't dilute averages used in scoring.
	appendIfPositive := func(dst *[]float64, p *float64) {
		if p != nil && *p > 0 {
			*dst = append(*dst, *p)
		}
	}

	// For the most recent day, daily_scores may be stale (backfill hasn't run
	// yet after a sync). Read fresh values from metric_points directly — they
	// are always up-to-date (INSERT writes there immediately).
	freshToday := s.freshDayFromRaw(lastDate)

	d := &health.RawMetrics{LastDate: lastDate}
	for i, r := range all {
		isLatest := i == 0
		if isLatest && freshToday != nil {
			// Override stale daily_scores with fresh hourly data for today.
			appendIfPositive(&d.HRV, coalesce(freshToday.hrv, r.hrv))
			appendIfPositive(&d.RHR, coalesce(freshToday.rhr, r.rhr))
			appendIfPositive(&d.Sleep, coalesce(freshToday.slp, r.slp))
			appendIfPositive(&d.Deep, coalesce(freshToday.deep, r.deep))
			appendIfPositive(&d.REM, coalesce(freshToday.rem, r.rem))
			appendIfPositive(&d.Awake, coalesce(freshToday.awake, r.awake))
			appendIfPositive(&d.Steps, coalesce(freshToday.steps, r.steps))
			appendIfPositive(&d.Cal, coalesce(freshToday.cal, r.cal))
			appendIfPositive(&d.Exercise, coalesce(freshToday.ex, r.ex))
			appendIfPositive(&d.SpO2, coalesce(freshToday.spo2, r.spo2))
			appendIfPositive(&d.VO2, coalesce(freshToday.vo2, r.vo2))
			appendIfPositive(&d.Resp, coalesce(freshToday.resp, r.resp))
		} else {
			appendIfPositive(&d.HRV, r.hrv)
			appendIfPositive(&d.RHR, r.rhr)
			appendIfPositive(&d.Sleep, r.slp)
			appendIfPositive(&d.Deep, r.deep)
			appendIfPositive(&d.REM, r.rem)
			appendIfPositive(&d.Awake, r.awake)
			appendIfPositive(&d.Steps, r.steps)
			appendIfPositive(&d.Cal, r.cal)
			appendIfPositive(&d.Exercise, r.ex)
			appendIfPositive(&d.SpO2, r.spo2)
			appendIfPositive(&d.VO2, r.vo2)
			appendIfPositive(&d.Resp, r.resp)
		}
	}

	// StepsWithDates and HRVWithDates — last 7 rows with actual data.
	for _, r := range all {
		if len(d.StepsWithDates) >= 7 {
			break
		}
		if r.steps != nil && *r.steps > 0 {
			d.StepsWithDates = append(d.StepsWithDates, health.DatedValue{Date: r.date, Val: *r.steps})
		}
		if r.hrv != nil && *r.hrv > 0 {
			d.HRVWithDates = append(d.HRVWithDates, health.DatedValue{Date: r.date, Val: *r.hrv})
		}
	}

	return d
}

// rawMetricsFromPoints reads raw metric time-series from metric_points using
// per-metric queries. This is the fallback path when daily_scores cache is cold.
func (s *DB) rawMetricsFromPoints(lastDate string) *health.RawMetrics {
	ctx := context.Background()

	getDailyValues := func(metric string, days int, agg string) []float64 {
		var err error
		var rows pgx.Rows
		if agg == "SUM" {
			sleepDedup := sleepDedupClause(metric)
			r, e := s.pool.Query(ctx, fmt.Sprintf(`
				SELECT MAX(source_sum)
				FROM (
					SELECT SUBSTRING(date,1,10) AS d, source, SUM(qty) AS source_sum
					FROM metric_points
					WHERE metric_name = $1 AND SUBSTRING(date,1,10) >= $2 AND qty > 0 %s
					GROUP BY d, source
				) sub
				GROUP BY d
				ORDER BY d DESC
				LIMIT $3`, sleepDedup),
				metric, subtractDays(lastDate, days), days)
			rows = r
			err = e
		} else {
			r, e := s.pool.Query(ctx, `
				SELECT `+agg+`(qty)
				FROM metric_points
				WHERE metric_name = $1 AND SUBSTRING(date,1,10) >= $2 AND qty > 0
				GROUP BY SUBSTRING(date,1,10)
				ORDER BY SUBSTRING(date,1,10) DESC
				LIMIT $3`,
				metric, subtractDays(lastDate, days), days)
			rows = r
			err = e
		}
		if err != nil {
			return nil
		}
		defer rows.Close()
		var vals []float64
		for rows.Next() {
			var v float64
			if rows.Scan(&v) == nil {
				vals = append(vals, v)
			}
		}
		return vals
	}

	getDailyWithDates := func(metric string, days int, agg string) []health.DatedValue {
		var err error
		var rows pgx.Rows
		if agg == "SUM" {
			sleepDedup := sleepDedupClause(metric)
			r, e := s.pool.Query(ctx, fmt.Sprintf(`
				SELECT d, MAX(source_sum)
				FROM (
					SELECT SUBSTRING(date,1,10) AS d, source, SUM(qty) AS source_sum
					FROM metric_points
					WHERE metric_name = $1 AND SUBSTRING(date,1,10) >= $2 AND qty > 0 %s
					GROUP BY d, source
				) sub
				GROUP BY d
				ORDER BY d DESC
				LIMIT $3`, sleepDedup),
				metric, subtractDays(lastDate, days), days)
			rows = r
			err = e
		} else {
			r, e := s.pool.Query(ctx, `
				SELECT SUBSTRING(date,1,10), `+agg+`(qty)
				FROM metric_points
				WHERE metric_name = $1 AND SUBSTRING(date,1,10) >= $2 AND qty > 0
				GROUP BY SUBSTRING(date,1,10)
				ORDER BY SUBSTRING(date,1,10) DESC
				LIMIT $3`,
				metric, subtractDays(lastDate, days), days)
			rows = r
			err = e
		}
		if err != nil {
			return nil
		}
		defer rows.Close()
		var out []health.DatedValue
		for rows.Next() {
			var dv health.DatedValue
			if rows.Scan(&dv.Date, &dv.Val) == nil {
				out = append(out, dv)
			}
		}
		return out
	}

	return &health.RawMetrics{
		LastDate:       lastDate,
		HRV:            getDailyValues("heart_rate_variability", 30, "AVG"),
		RHR:            getDailyValues("resting_heart_rate", 30, "AVG"),
		Sleep:          getDailyValues("sleep_total", 30, "SUM"),
		Deep:           getDailyValues("sleep_deep", 30, "SUM"),
		REM:            getDailyValues("sleep_rem", 30, "SUM"),
		Awake:          getDailyValues("sleep_awake", 30, "SUM"),
		Steps:          getDailyValues("step_count", 30, "SUM"),
		Cal:            getDailyValues("active_energy", 30, "SUM"),
		Exercise:       getDailyValues("apple_exercise_time", 30, "SUM"),
		SpO2:           getDailyValues("blood_oxygen_saturation", 30, "AVG"),
		VO2:            getDailyValues("vo2_max", 30, "AVG"),
		Resp:           getDailyValues("respiratory_rate", 30, "AVG"),
		WristTemp:      getDailyValues("wrist_temperature", 30, "AVG"),
		StepsWithDates: getDailyWithDates("step_count", 7, "SUM"),
		HRVWithDates:   getDailyWithDates("heart_rate_variability", 7, "AVG"),
	}
}

// GetHealthBriefing fetches raw metric time series from the DB and delegates
// all scoring and insight computation to the health package.
// lang selects the output language ("en", "ru", "sr").
func (s *DB) GetHealthBriefing(lang string) (*health.BriefingResponse, error) {
	ctx := context.Background()
	// Use hourly_metrics for lastDate — avoids full scan of metric_points.
	var lastDate *string
	if err := s.pool.QueryRow(ctx, `SELECT MAX(SUBSTRING(hour,1,10)) FROM hourly_metrics`).Scan(&lastDate); err != nil || lastDate == nil {
		return &health.BriefingResponse{Greeting: "Welcome! No health data yet."}, nil
	}

	// Try reading 30-day metric history from daily_scores (1 query).
	// Fall back to per-metric queries against metric_points if cache is cold.
	data := s.rawMetricsFromDailyScores(*lastDate)
	if data == nil {
		data = s.rawMetricsFromPoints(*lastDate)
	}

	// Supplement wrist_temperature (not in daily_scores) for anomaly detection.
	if len(data.WristTemp) == 0 {
		data.WristTemp = s.fetchDailyMetric("wrist_temperature", *lastDate, 30, "AVG")
	}

	resp := health.ComputeBriefing(*data, lang)

	// Attach per-source sleep breakdown for the most recent night.
	// Query hourly_metrics (indexed by hour) instead of metric_points.
	if resp.Sleep != nil {
		sleepRows, qErr := s.pool.Query(ctx, `
			SELECT source,
				SUM(CASE WHEN metric_name='sleep_total' THEN avg_val ELSE 0 END),
				SUM(CASE WHEN metric_name='sleep_deep'  THEN avg_val ELSE 0 END),
				SUM(CASE WHEN metric_name='sleep_rem'   THEN avg_val ELSE 0 END),
				SUM(CASE WHEN metric_name='sleep_core'  THEN avg_val ELSE 0 END),
				SUM(CASE WHEN metric_name='sleep_awake' THEN avg_val ELSE 0 END)
			FROM hourly_metrics
			WHERE metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_awake')
			  AND SUBSTRING(hour,1,10) = $1
			GROUP BY source
			ORDER BY SUM(CASE WHEN metric_name='sleep_total' THEN avg_val ELSE 0 END) DESC`,
			*lastDate)
		if qErr == nil {
			defer sleepRows.Close()
			for sleepRows.Next() {
				var ss health.SleepSourceSummary
				if sErr := sleepRows.Scan(&ss.Source, &ss.Total, &ss.Deep, &ss.REM, &ss.Core, &ss.Awake); sErr == nil && ss.Total > 0 {
					resp.Sleep.Sources = append(resp.Sleep.Sources, ss)
				}
			}
		}
	}
	return resp, nil
}

// GetReadinessHistory returns readiness scores for the last `outputDays` days.
// Results are served from the daily_scores cache when available and fresh;
// otherwise the full sliding-window computation runs and the cache is updated.
func (s *DB) GetReadinessHistory(outputDays int) ([]health.ReadinessPoint, error) {
	cached, err := s.readinessFromCache(outputDays)
	if err == nil && isCacheRecent(cached) {
		return cached, nil
	}
	pts, err := s.computeReadinessHistory(outputDays)
	if err != nil {
		return nil, err
	}
	go s.saveReadinessScores(pts)
	return pts, nil
}

// computeReadinessHistory is the raw sliding-window computation (no caching).
// For each output day D it uses HRV/RHR/sleep data from D-29..D
// (most-recent-first) and calls health.ComputeReadinessScore.
func (s *DB) computeReadinessHistory(outputDays int) ([]health.ReadinessPoint, error) {
	ctx := context.Background()
	window := 30
	total := outputDays + window

	// Determine the latest date from data (not server time) to avoid TZ mismatch.
	var lastDate *string
	s.pool.QueryRow(ctx, `SELECT MAX(SUBSTRING(date,1,10)) FROM metric_points`).Scan(&lastDate)
	if lastDate == nil {
		return nil, fmt.Errorf("no metric data found")
	}
	fromDate := subtractDays(*lastDate, total)

	// Fetch date-keyed maps for the full look-back period.
	fetch := func(metric, agg string, isSleep bool) (map[string]float64, error) {
		var pgxRows pgx.Rows
		var err error
		if isSleep {
			r, e := s.pool.Query(ctx, `
				SELECT d, MAX(source_sum)
				FROM (
					SELECT SUBSTRING(date,1,10) AS d, source, SUM(qty) AS source_sum
					FROM metric_points
					WHERE metric_name = $1
					  AND qty > 0
					  AND SUBSTRING(date,1,10) >= $2
					GROUP BY d, source
				) sub
				GROUP BY d`,
				metric, fromDate)
			pgxRows = r
			err = e
		} else {
			r, e := s.pool.Query(ctx, `
				SELECT SUBSTRING(date,1,10), `+agg+`(qty)
				FROM metric_points
				WHERE metric_name = $1
				  AND qty > 0
				  AND SUBSTRING(date,1,10) >= $2
				GROUP BY SUBSTRING(date,1,10)`,
				metric, fromDate)
			pgxRows = r
			err = e
		}
		if err != nil {
			return nil, err
		}
		defer pgxRows.Close()
		m := make(map[string]float64)
		for pgxRows.Next() {
			var d string
			var v float64
			if pgxRows.Scan(&d, &v) == nil {
				m[d] = v
			}
		}
		return m, nil
	}

	hrvMap, err := fetch("heart_rate_variability", "AVG", false)
	if err != nil {
		return nil, err
	}
	rhrMap, err := fetch("resting_heart_rate", "AVG", false)
	if err != nil {
		return nil, err
	}
	sleepMap, err := fetch("sleep_total", "SUM", true)
	if err != nil {
		return nil, err
	}

	// Build a sorted list of all days we have any data for.
	dateSet := make(map[string]bool)
	for d := range hrvMap {
		dateSet[d] = true
	}
	for d := range rhrMap {
		dateSet[d] = true
	}
	for d := range sleepMap {
		dateSet[d] = true
	}
	allDates := make([]string, 0, len(dateSet))
	for d := range dateSet {
		allDates = append(allDates, d)
	}
	sort.Strings(allDates)

	// For each output day (last outputDays dates) compute score using 30-day window.
	if len(allDates) > outputDays {
		allDates = allDates[len(allDates)-outputDays:]
	}

	// valsBefore returns values for all dates <= anchor, sorted by DATE descending
	// (most recent first) so that vals[:3] is the last 3 days, vals[3:] is the
	// historical baseline. Sorting by value (as before) was a bug: it put the
	// best HRV days first, artificially inflating the "recent" average.
	valsBefore := func(m map[string]float64, anchor string) []float64 {
		type dateval struct{ d string; v float64 }
		var pairs []dateval
		for d, v := range m {
			if d <= anchor {
				pairs = append(pairs, dateval{d, v})
			}
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].d > pairs[j].d })
		if len(pairs) > window {
			pairs = pairs[:window]
		}
		out := make([]float64, len(pairs))
		for i, p := range pairs {
			out[i] = p.v
		}
		return out
	}

	out := make([]health.ReadinessPoint, 0, len(allDates))
	for _, d := range allDates {
		hrv := valsBefore(hrvMap, d)
		rhr := valsBefore(rhrMap, d)
		sleep := valsBefore(sleepMap, d)
		score := health.ComputeReadinessScore(hrv, rhr, sleep)
		out = append(out, health.ReadinessPoint{Date: d, Score: score})
	}
	return out, nil
}

// fetchDailyMetric reads a single metric's daily values from metric_points.
// Used for metrics not stored in daily_scores (e.g. wrist_temperature).
func (s *DB) fetchDailyMetric(metric, lastDate string, days int, agg string) []float64 {
	rows, err := s.pool.Query(context.Background(), `
		SELECT `+agg+`(qty)
		FROM metric_points
		WHERE metric_name = $1 AND SUBSTRING(date,1,10) >= $2 AND qty > 0
		GROUP BY SUBSTRING(date,1,10)
		ORDER BY SUBSTRING(date,1,10) DESC
		LIMIT $3`,
		metric, subtractDays(lastDate, days), days)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var vals []float64
	for rows.Next() {
		var v float64
		if rows.Scan(&v) == nil {
			vals = append(vals, v)
		}
	}
	return vals
}

// dayRow mirrors the daily_scores column set, used for fresh-day override.
type dayRow struct {
	hrv, rhr, slp, deep, rem, core, awake *float64
	steps, cal, ex, spo2, vo2, resp       *float64
}

// freshDayFromRaw reads today's values directly from metric_points (always
// up-to-date, unlike hourly_metrics which may be stale after cache invalidation).
// Uses smart combine for SUM metrics (pipe-source aware dedup).
func (s *DB) freshDayFromRaw(date string) *dayRow {
	ctx := context.Background()
	type spec struct {
		metric string
		dest   **float64
		isSum  bool
	}
	r := &dayRow{}
	specs := []spec{
		{"heart_rate_variability", &r.hrv, false},
		{"resting_heart_rate", &r.rhr, false},
		{"sleep_total", &r.slp, true},
		{"sleep_deep", &r.deep, true},
		{"sleep_rem", &r.rem, true},
		{"sleep_core", &r.core, true},
		{"sleep_awake", &r.awake, true},
		{"step_count", &r.steps, true},
		{"active_energy", &r.cal, true},
		{"apple_exercise_time", &r.ex, true},
		{"blood_oxygen_saturation", &r.spo2, false},
		{"vo2_max", &r.vo2, false},
		{"respiratory_rate", &r.resp, false},
	}
	anyFound := false
	for _, sp := range specs {
		var val float64
		var err error
		if sp.isSum {
			sleepDedup := sleepDedupClause(sp.metric)
			err = s.pool.QueryRow(ctx, fmt.Sprintf(`
				SELECT COALESCE(%s, 0) FROM (
					SELECT source, SUM(qty) AS source_sum
					FROM metric_points
					WHERE metric_name=$1 AND SUBSTRING(date,1,10)=$2 AND qty > 0 %s
					GROUP BY source
				) sub`, sumCombineExpr("source_sum"), sleepDedup), sp.metric, date).Scan(&val)
		} else {
			err = s.pool.QueryRow(ctx, `
				SELECT COALESCE(AVG(qty), 0)
				FROM metric_points
				WHERE metric_name=$1 AND SUBSTRING(date,1,10)=$2 AND qty > 0`,
				sp.metric, date).Scan(&val)
		}
		if err == nil && val > 0 {
			v := val
			*sp.dest = &v
			anyFound = true
		}
	}
	if !anyFound {
		return nil
	}
	return r
}

// coalesce returns the first non-nil pointer, or nil if both are nil.
func coalesce(a, b *float64) *float64 {
	if a != nil {
		return a
	}
	return b
}

func subtractDays(dateStr string, days int) string {
	t, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return dateStr
	}
	return t.AddDate(0, 0, -days).Format("2006-01-02")
}
