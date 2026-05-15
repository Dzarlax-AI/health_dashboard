package storage

import (
	"strings"
	"time"
)

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
//   - Source priority: same as sleep dedup elsewhere (Apple Watch > RingConn).
//     Single source per night, picked by total duration in the window.
//   - Each segment expands to a [start, start+qty hours] minute interval
//     and OR-merges into a per-day [1440]bool mask. Cross-midnight segments
//     spill into the next day.
//   - Day comparisons go consecutive-pair: (d1,d2), (d2,d3) … so the
//     denominator is (n-1)×1440 minute-pairs.
func (s *DB) ComputeSRI(days int) (sri float64, nights int, ok bool) {
	if days < 7 {
		days = 7
	}
	ctx, cancel := queryCtx()
	defer cancel()

	// Pull a slightly wider window than `days` so the *first* day in the
	// requested window has a previous-day reference for the consecutive-pair
	// computation (otherwise the first day's pairs are dropped silently and
	// the user-visible "14d SRI" only sees 13 day-pairs).
	rows, err := s.pool.Query(ctx, `
		SELECT date, qty, source, metric_name
		FROM metric_points
		WHERE metric_name IN ('sleep_total','sleep_deep','sleep_rem','sleep_core','sleep_unspecified')
		  AND qty > 0
		  AND quality = 'ok'
		  AND SUBSTRING(date, 12, 8) != '00:00:00'
		  AND SUBSTRING(date, 1, 10) >= TO_CHAR(NOW() - INTERVAL '1 day' * $1, 'YYYY-MM-DD')
		ORDER BY date ASC`,
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

	// Per-day source pick: prefer Apple Watch family if present with non-
	// trivial total, else highest-total source. Mirrors the priority used in
	// sleepCrossValidationPickExpr (Apple Watch > RingConn > anything else).
	bestSource := map[string]string{}
	for d, srcMap := range totalsBySrc {
		var watchSrc string
		var fallbackSrc string
		var fallbackTotal float64
		for src, total := range srcMap {
			if (strings.Contains(src, "Ultra") || strings.Contains(src, "Apple Watch")) && total > 1.0 {
				if watchSrc == "" || total > srcMap[watchSrc] {
					watchSrc = src
				}
			}
			if total > fallbackTotal {
				fallbackTotal = total
				fallbackSrc = src
			}
		}
		if watchSrc != "" {
			bestSource[d] = watchSrc
		} else {
			bestSource[d] = fallbackSrc
		}
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

// parseSleepDate parses metric_points.date in the canonical
// "YYYY-MM-DD HH:MM:SS ±TZ" form.
func parseSleepDate(s string) (time.Time, error) {
	return time.Parse("2006-01-02 15:04:05 -0700", s)
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
