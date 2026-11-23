package storage

import (
	"encoding/json"
	"strconv"
	"time"

	dbtmetrics "health-receiver/experiments/dbt_metrics"
)

// ComputeSRI compares the stored local sleep minutes on consecutive observed days.
func (s *DB) ComputeSRI(days int) (float64, int, bool) {
	ctx, cancel := queryCtx()
	defer cancel()
	preferred, err := readPreferredSleepSource(ctx, s.pool)
	if err != nil {
		return 0, 0, false
	}
	if days < 7 {
		days = 7
	}
	rows, err := s.pool.Query(ctx, `select date,source,metric_name,qty from metric_points
		where metric_name in ('sleep_total','sleep_deep','sleep_core','sleep_unspecified','sleep_rem')
		  and quality='ok' and qty>0 and source is not null and substring(date,12,8)<>'00:00:00'
		  and substring(date,1,10)>=to_char(now()-interval '1 day' * $1,'YYYY-MM-DD')`, days+1)
	if err != nil {
		return 0, 0, false
	}
	defer rows.Close()
	// Keep the storage clock parser: local minutes must survive travel,
	// subsecond timestamps and older fixed-offset strings exactly as recorded.
	type segment struct {
		Wall   string `json:"observed_at"`
		Source string `json:"source"`
		Metric string `json:"metric_name"`
		Qty    string `json:"qty"`
	}
	segments := []segment{}
	for rows.Next() {
		var date string
		var qty float64
		var point segment
		if err := rows.Scan(&date, &point.Source, &point.Metric, &qty); err != nil {
			continue
		}
		at, err := parseSleepDate(date)
		if err != nil {
			continue
		}
		point.Wall = at.Format("2006-01-02 15:04:00")
		point.Qty = strconv.FormatFloat(qty, 'g', -1, 64)
		segments = append(segments, point)
	}
	if rows.Err() != nil {
		return 0, 0, false
	}
	rows.Close()
	payload, err := json.Marshal(segments)
	if err != nil {
		return 0, 0, false
	}
	var score *float64
	var nights int
	err = s.pool.QueryRow(ctx, `with captures as (
		select ''::text as profile,''::text as capture,$2::text as preferred_source,$1::int as days
	),segments as (
		select c.*,s.* from captures c cross join jsonb_to_recordset($3::jsonb)
		s(observed_at timestamp,source text,metric_name text,qty float8)
	) select nights,sri from (`+dbtmetrics.SleepRegularitySQL+`) result`, days, preferred, json.RawMessage(payload)).Scan(&nights, &score)
	if err != nil || score == nil {
		return 0, nights, false
	}
	return *score, nights, true
}

// parseSleepDate parses metric_points.date in the canonical
// "YYYY-MM-DD HH:MM:SS ±TZ" form.
func parseSleepDate(s string) (time.Time, error) {
	return time.Parse("2006-01-02 15:04:05 -0700", s)
}
