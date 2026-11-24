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
	var anchor, cutoff string
	if err := s.pool.QueryRow(ctx, `select to_char(now(),'YYYY-MM-DD'),to_char(now()-interval '1 day'*$1,'YYYY-MM-DD')`, days+1).Scan(&anchor, &cutoff); err != nil {
		return 0, 0, false
	}
	rows, err := s.pool.Query(ctx, `select date,source,metric_name,qty from metric_points
		where metric_name in ('sleep_total','sleep_deep','sleep_core','sleep_unspecified','sleep_rem')
		  and quality='ok' and qty>0 and source is not null and substring(date,12,8)<>'00:00:00'
		  and substring(date,1,10)>=$1`, cutoff)
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
	type result struct {
		score  *float64
		nights int
	}
	out, err := withDecisionJournal(ctx, s, func(db *DB, journal bool) (result, error) {
		query := `with captures as (
			select ''::text as profile,''::text as capture,$2::text as preferred_source,$1::int as days
		),segments as (
			select c.*,s.* from captures c cross join jsonb_to_recordset($3::jsonb)
			s(observed_at timestamp,source text,metric_name text,qty float8)
		), computed as (` + dbtmetrics.SleepRegularitySQL + `)`
		args := []any{days, preferred, json.RawMessage(payload)}
		if journal {
			query += sriReceiptSQL + decisionJournalInsertSQL
			args = append(args, anchor)
		}
		var r result
		err := db.pool.QueryRow(ctx, query+" select nights,sri from computed", args...).Scan(&r.nights, &r.score)
		return r, err
	})
	if err != nil || out.score == nil {
		return 0, out.nights, false
	}
	return *out.score, out.nights, true
}

// Only local wall-clock minutes enter SRI. Sorting the retained rows makes
// repeated input selections stable while preserving duplicate observations.
const sriReceiptSQL = `, daily_inputs as (
	select substring(item->>'observed_at',1,10) as day,
		jsonb_build_object('segments',jsonb_agg(item order by item)) inputs
	from jsonb_array_elements($3::jsonb) item group by 1
), versions as (
	select *,encode(sha256(convert_to(jsonb_build_array(1,inputs)::text,'UTF8')),'hex') hash from daily_inputs
), decision_packets as (
	select 'sri_day'::text metric,day decision_key,1 formula_version,inputs,'{}'::jsonb result from versions
	union all
	select 'sri_window',$4::text||'/'||$1::int,1,
		jsonb_build_object('window_days',$1::int,'preferred_source',$2::text,'revisions',
			(select coalesce(jsonb_object_agg(day,hash),'{}'::jsonb) from versions)),
		jsonb_build_object('nights',nights,'sri',sri) from computed
)`

// parseSleepDate parses metric_points.date in the canonical
// "YYYY-MM-DD HH:MM:SS ±TZ" form.
func parseSleepDate(s string) (time.Time, error) {
	return time.Parse("2006-01-02 15:04:05 -0700", s)
}
