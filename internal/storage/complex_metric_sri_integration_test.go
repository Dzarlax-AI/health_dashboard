package storage

import (
	"context"
	"math"
	"os"
	"testing"
	"time"
)

func TestSleepRegularityShadow(t *testing.T) {
	sourceSchema, graphSchema := "briefing_pilot", "derived"
	if os.Getenv("PREFERRED_SLEEP_FIXTURE_TOKEN") != "" {
		sourceSchema, graphSchema = "source_data", "dbt_output"
	} else if os.Getenv("BRIEFING_STORAGE_PILOT") != "1" {
		t.Skip("synthetic dbt capture required")
	}
	db, cleanup := testDB(t)
	defer cleanup()
	id := insertTestRawRecord(t, db, "sri-synthetic")
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into metric_points(health_record_id,metric_name,units,date,qty,source,quality)
        select $1, metric,'hr',(current_date-n)::text || wall_time, qty, source,'ok'
        from generate_series(2,13) n cross join (values
          ('sleep_total',' 23:30:00 +0200',2.0,'Apple Watch'),
          ('sleep_total',' 21:10:00 +0200',4.0,'Ring'),
          ('sleep_total',' 00:00:00 +0200',7.0,'Summary'),
          ('sleep_deep',' 03:10:00 +0200',0.5,'Apple Watch')
        ) s(metric,wall_time,qty,source) where n<>6`, id)
	compare := func(capture string) float64 {
		t.Helper()
		exec(`insert into `+sourceSchema+`.sri_captures values('synthetic',$1,current_date,14,$2)`, capture, db.GetSetting(PreferredSleepSourceSetting, ""))
		exec(`insert into `+sourceSchema+`.sri_segments select 'synthetic',$1,left(date,19)::timestamp,source,metric_name,qty,quality from metric_points where health_record_id=$2`, capture, id)
		want, n, ok := db.referenceSRI(14)
		live, liveDays, available := db.ComputeSRI(14)
		if ok != available || n != liveDays || math.Abs(live-want) > 1e-9 {
			t.Fatalf("live %s: %v %d %t; previous %v %d %t", capture, live, liveDays, available, want, n, ok)
		}
		var got *float64
		var nights *int
		var status string
		if err := db.pool.QueryRow(ctx, `select sri,nights,status from `+graphSchema+`.sleep_regularity_index_v1 where profile='synthetic' and capture=$1`, capture).Scan(&got, &nights, &status); err != nil {
			t.Fatal(err)
		}
		if nights == nil || n != *nights || ok != (got != nil) || ok && (status != "available" || math.Abs(*got-want) > 1e-9) || !ok && status != "insufficient_days" {
			t.Fatalf("capture %s: Go (%v,%d,%t), SQL (%v,%v,%s)", capture, want, n, ok, got, nights, status)
		}
		return want
	}
	compare("mixed_sources_and_gap")
	exec(`update metric_points set qty=3.7 where source='Apple Watch' and metric_name='sleep_total' and left(date,10)=(current_date-4)::text`)
	winnerScore := compare("late_correction")
	exec(`insert into metric_points(health_record_id,metric_name,units,date,qty,source,quality)
        select health_record_id,metric_name,units,replace(date,'23:30:00','13:30:00'),qty,'Ä Ultra',quality
        from metric_points where source='Apple Watch' and metric_name='sleep_total'`)
	// With the rival present the score must match the Apple Watch alone.
	if got := compare("equal_rank"); got != winnerScore {
		t.Fatalf("tie selected a different source: got %v, want %v", got, winnerScore)
	}
	if err := db.SaveSettings(map[string]string{PreferredSleepSourceSetting: "Ä Ultra"}); err != nil {
		t.Fatal(err)
	}
	preferredScore := compare("preferred_rival")
	if err := db.SaveSettings(map[string]string{PreferredSleepSourceSetting: "unobserved"}); err != nil {
		t.Fatal(err)
	}
	if got := compare("preferred_missing"); got != winnerScore {
		t.Fatalf("missing source changed SRI: %v vs %v", got, winnerScore)
	}
	exec(`delete from metric_points where source='Apple Watch'`)
	if rival := compare("other_source_only"); rival == winnerScore || rival != preferredScore {
		t.Fatal("fixture should distinguish the two source-specific sleep masks")
	}
	tx, err := db.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "alter table settings rename to settings_unavailable"); err != nil {
		t.Fatal(err)
	}
	unit := &DB{pool: &cacheTransaction{Tx: tx, ctx: ctx}}
	if _, _, ok := unit.ComputeSRI(14); ok {
		t.Fatal("SRI should be unavailable if the source preference cannot be read")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	exec(`delete from metric_points where left(date,10)<(current_date-3)::text`)
	compare("withdrawal")
	exec("delete from metric_points where health_record_id=$1", id)
	exec(`insert into metric_points(health_record_id,metric_name,units,date,qty,source,quality)
	     select $1,'sleep_core','hr',(current_date-n)::text||' 23:59:59 -0430',0.125,'Phone','ok'
	     from generate_series(2,6) n`, id)
	compare("six_observed_days")
	if _, n, ok := db.ComputeSRI(14); n != 6 || ok {
		t.Fatalf("six-day gate %d/%t", n, ok)
	}
	exec(`insert into metric_points(health_record_id,metric_name,units,date,qty,source,quality)
	     values ($1,'sleep_core','hr',(current_date-7)::text||' 04:01:00 +0530',0.125,'Phone','ok')`, id)
	compare("seven_observed_days")
	if _, n, ok := db.ComputeSRI(6); n != 7 || !ok {
		t.Fatalf("seven-day gate %d/%t", n, ok)
	}

}

// ComputeSRI returns the Sleep Regularity Index (Phillips & Czeisler 2017)
// over the last `days` calendar days. The algorithm:
//
//	SRI = 200 × p − 100, where p = fraction of (minute, day) pairs whose
//	      sleep/wake state matches the same minute on the previous day.
//
// Range: 0 = random, 100 = perfectly regular. Empirical UK Biobank 2025
// thresholds (SCORING.md ref 33): SRI > 75 protective, < 50 clinically
// irregular. Negative values are theoretically possible (anti-correlated
// with prior day) but practically vanishing.
//
// Returns (sri, nightsWithData, true) on success. (0, n, false) when fewer
// than 7 calendar days carry per-segment sleep data (`date` HH:MM != 00:00:00,
// the iOS-pushed format) — driven by HAE midnight-summary nights alone, the
// SRI is undefined because we don't know *when* sleep occurred.
//
// Implementation notes:
//   - Loads sleep_total, sleep_deep, sleep_rem, sleep_core, sleep_unspecified
//     fragments (any "asleep" stage). sleep_awake fragments do NOT count
//     as asleep.
//   - Source priority: qualifying Apple Watch family, then highest-total source.
//     Single source per night, picked by total duration in the window.
//   - Each segment expands to a [start, start+qty hours] minute interval
//     and OR-merges into a per-day [1440]bool mask. Cross-midnight segments
//     spill into the next day.
//   - Day comparisons go consecutive-pair: (d1,d2), (d2,d3) … so the
//     denominator is (n-1)×1440 minute-pairs.
func (s *DB) referenceSRI(days int) (sri float64, nights int, ok bool) {
	if days < 7 {
		days = 7
	}
	ctx, cancel := queryCtx()
	defer cancel()

	// Pull a slightly wider window than `days` so the *first* day in the
	// requested window has a previous-day reference for the consecutive-pair
	// computation (otherwise the first day's pairs are dropped silently and
	// the user-visible "14d SRI" only sees 13 day-pairs).
	preferred, err := readPreferredSleepSource(ctx, s.pool)
	if err != nil {
		return 0, 0, false
	}
	rows, err := s.pool.Query(ctx, `
		SELECT date, qty, source, metric_name
		FROM metric_points
		WHERE metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_unspecified')
		  AND qty > 0
		  AND quality = 'ok'
		  AND SUBSTRING(date, 12, 8) != '00:00:00'
		  AND SUBSTRING(date, 1, 10) >= TO_CHAR(NOW() - INTERVAL '1 day' * $1, 'YYYY-MM-DD')
		ORDER BY qty ASC, date ASC`,
		days+1)
	if err != nil {
		return 0, 0, false
	}
	defer rows.Close()

	type seg struct {
		start  time.Time
		hours  float64
		source string
		metric string
	}
	all := []seg{}
	// Per-day, per-source total of sleep_total — used to pick the winning
	// source for that night. Phase rows (sleep_deep/rem/core) don't drive
	// the pick; they only get painted into the mask if their source matches
	// the winning source for that day.
	totalsBySrc := map[string]map[string]float64{} // day → source → sum(sleep_total)
	for rows.Next() {
		var dateStr string
		var qty float64
		var source, metric string
		if err := rows.Scan(&dateStr, &qty, &source, &metric); err != nil {
			continue
		}
		t, perr := parseSleepDate(dateStr)
		if perr != nil {
			continue
		}
		all = append(all, seg{start: t, hours: qty, source: source, metric: metric})
		if metric == "sleep_total" {
			d := t.Format("2006-01-02")
			if totalsBySrc[d] == nil {
				totalsBySrc[d] = map[string]float64{}
			}
			totalsBySrc[d][source] += qty
		}
	}
	if rows.Err() != nil {
		return 0, 0, false
	}

	bestSource := map[string]string{}
	for day, totals := range totalsBySrc {
		bestSource[day], _ = chooseSleepSource(totals, false, preferred)
	}

	// Build per-calendar-day [1440]bool asleep mask, OR-merging stages.
	// Cross-midnight segments spill into next day.
	masks := map[string][1440]bool{}
	for _, sg := range all {
		d := sg.start.Format("2006-01-02")
		if bestSource[d] != "" && sg.source != bestSource[d] {
			continue
		}
		paintMask(masks, sg.start, sg.hours)
	}
	if len(masks) < 7 {
		return 0, len(masks), false
	}

	// Consecutive-pair compare across sorted dates within the requested
	// window (the SQL pulled days+1 days; trim to days).
	sortedDates := make([]string, 0, len(masks))
	for d := range masks {
		sortedDates = append(sortedDates, d)
	}
	sortStringsAsc(sortedDates)
	if len(sortedDates) > days {
		sortedDates = sortedDates[len(sortedDates)-days:]
	}

	totalPairs := 0
	matchPairs := 0
	for i := 1; i < len(sortedDates); i++ {
		prev := masks[sortedDates[i-1]]
		cur := masks[sortedDates[i]]
		for m := 0; m < 1440; m++ {
			totalPairs++
			if prev[m] == cur[m] {
				matchPairs++
			}
		}
	}
	if totalPairs == 0 {
		return 0, len(sortedDates), false
	}
	p := float64(matchPairs) / float64(totalPairs)
	sri = 200*p - 100
	return sri, len(sortedDates), true
}

// paintMask OR-merges a sleep segment of `hours` length starting at `start`
// into the per-day minute mask map. Splits on midnight when needed.
func paintMask(masks map[string][1440]bool, start time.Time, hours float64) {
	totalMin := int(hours*60 + 0.5)
	if totalMin <= 0 {
		return
	}
	cursor := start
	for totalMin > 0 {
		dayKey := cursor.Format("2006-01-02")
		mask := masks[dayKey]
		minOfDay := cursor.Hour()*60 + cursor.Minute()
		end := minOfDay + totalMin
		if end > 1440 {
			end = 1440
		}
		for m := minOfDay; m < end; m++ {
			mask[m] = true
		}
		masks[dayKey] = mask
		consumed := end - minOfDay
		totalMin -= consumed
		// roll cursor to start of next calendar day
		cursor = cursor.Add(time.Duration(consumed) * time.Minute)
	}
}

// sortStringsAsc is a tiny helper avoiding the sort package import bloat in
// this file (the rest of the file uses zero stdlib helpers beyond time).
func sortStringsAsc(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
