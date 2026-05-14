package storage

import (
	"sort"
	"strings"
	"time"
)

// WakeTimeForDate returns the wake-time and sleep-onset hours-of-day (in
// local `loc`) for the night ending on `date` and the night starting on
// `date`, respectively. This is the per-date analogue of
// GetTypicalWakeTime (which averages across N days and is the wrong shape
// for v2.2 stress methodology, see STRESS_MEASUREMENT.md §0 blocker 2).
//
// Algorithm (per STRESS_MEASUREMENT.md §0 blocker 2):
//  1. Pull asleep-state segments (sleep_deep, sleep_rem, sleep_core) for
//     a wide window around date d. sleep_awake is wake, not asleep, and
//     sleep_total is an aggregate that double-counts — neither is loaded.
//  2. Pick the winning source for the period (Apple Watch family >
//     RingConn > anything else by total hours), mirroring the priority
//     used in sri.go / sleepCrossValidationPickExpr.
//  3. Build [start, end] intervals from the winning source, sort, and
//     merge runs with gap ≤ mergeTolerance (default 30min — a brief
//     mid-night arousal does not split the main sleep).
//  4. For wake_time: find the merged run whose MIDPOINT falls within ±6h
//     of local midnight ending date d. If multiple qualify, pick the
//     longest; tie-break by later-ending end (matches spec "equally-long
//     sleeps split around midnight → pick the one whose end is later").
//     wake_time[d] = end of that run.
//  5. For sleep_onset: same algorithm anchored to local midnight ending
//     date d+1 (i.e. the start of the *next* night). onset = start of
//     that run.
//  6. If either is missing → fallback BOTH to default 07:00 / 22:00 and
//     set imputed=true. ok=false only on hard DB error or unparseable
//     date input.
//
// Known limitation per spec edge case: a strict ±6h window does NOT
// accommodate true shift work (sleep 06:00→14:00, midpoint 10:00 sits
// 10h from midnight). The spec's example claiming otherwise is
// inconsistent with the literal rule; this implementation follows the
// rule. Shift-worker nights fall through to imputed=true. Revisit when
// shift-work cohort data is available.
//
// loc must be the tenant's REPORT_TZ. The function does not consult
// REPORT_TZ itself because callers (notably energy_v2_orchestrator) are
// per-tenant and already hold their loc.
func (s *DB) WakeTimeForDate(date string, loc *time.Location) (wakeHour, sleepOnsetHour float64, imputed bool, ok bool) {
	if loc == nil {
		loc = time.UTC
	}
	d, err := time.ParseInLocation("2006-01-02", date, loc)
	if err != nil {
		return 0, 0, false, false
	}
	midnightD := d                                    // 00:00 local of date d (start of d)
	midnightDPlus1 := d.AddDate(0, 0, 1)              // 00:00 local of d+1
	// Wide query window: d-1 12:00 local through d+2 12:00 local. Covers
	// the night ending on d (likely starts d-1 evening) and the night
	// starting on d (likely ends d+1 morning), with margin for late
	// risers / late sleepers.
	winStart := d.AddDate(0, 0, -1).Add(12 * time.Hour)
	winEnd := d.AddDate(0, 0, 2).Add(12 * time.Hour)

	segs, qerr := s.fetchAsleepSegments(winStart, winEnd)
	if qerr != nil {
		// Hard DB error — caller should treat as not-ok and fall back
		// to the static cap on its own. Do not fabricate hours.
		return 0, 0, false, false
	}

	wake, wakeOK := pickMainSleep(segs, midnightD)
	onset, onsetOK := pickMainSleep(segs, midnightDPlus1)
	if !wakeOK || !onsetOK {
		// imputed_awake_window flag path. Both fields fall back together
		// so downstream consumers don't mix real + fake on the same day.
		return 7.0, 22.0, true, true
	}
	wakeLocal := wake.End.In(loc)
	onsetLocal := onset.Start.In(loc)
	return hourOfDay(wakeLocal), hourOfDay(onsetLocal), false, true
}

// sleepSegment is a merged asleep-state interval.
type sleepSegment struct {
	Start time.Time
	End   time.Time
}

// Duration of the merged segment.
func (sg sleepSegment) Duration() time.Duration { return sg.End.Sub(sg.Start) }

// Midpoint of the merged segment.
func (sg sleepSegment) Midpoint() time.Time {
	return sg.Start.Add(sg.End.Sub(sg.Start) / 2)
}

// mergeTolerance is the maximum gap between consecutive asleep segments
// that is still considered part of the same main sleep. 30 minutes
// tolerates brief mid-night arousals (toilet, glance at phone) without
// splitting an 8h night into "two short sleeps".
const mergeTolerance = 30 * time.Minute

// midnightWindow is the ±range around local midnight in which a merged
// run's midpoint must fall to be considered "the main sleep" for the
// night ending/starting at that midnight. See spec §0 blocker 2.
const midnightWindow = 6 * time.Hour

// rawSleepSegment is one row from metric_points — a single sleep-stage
// fragment with its source. We pick the winning source first, then
// merge.
type rawSleepSegment struct {
	Start  time.Time
	End    time.Time
	Source string
}

// fetchAsleepSegments queries metric_points for asleep-state fragments
// in [winStart, winEnd] (inclusive of start, exclusive of end), picks
// the winning source by total hours, and returns merged sleepSegments.
func (s *DB) fetchAsleepSegments(winStart, winEnd time.Time) ([]sleepSegment, error) {
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT date, qty, source
		  FROM metric_points
		 WHERE metric_name IN ('sleep_deep','sleep_rem','sleep_core')
		   AND qty > 0
		   AND quality = 'ok'
		   AND SUBSTRING(date, 12, 8) != '00:00:00'
		   AND SUBSTRING(date, 1, 10) >= TO_CHAR($1::timestamptz, 'YYYY-MM-DD')
		   AND SUBSTRING(date, 1, 10) <= TO_CHAR($2::timestamptz, 'YYYY-MM-DD')
		 ORDER BY date ASC`, winStart, winEnd)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var raws []rawSleepSegment
	for rows.Next() {
		var dateStr, source string
		var qty float64
		if err := rows.Scan(&dateStr, &qty, &source); err != nil {
			continue
		}
		start, perr := parseSleepDate(dateStr)
		if perr != nil {
			continue
		}
		end := start.Add(time.Duration(qty * float64(time.Hour)))
		if !end.After(winStart) || !start.Before(winEnd) {
			continue
		}
		raws = append(raws, rawSleepSegment{Start: start, End: end, Source: source})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return mergeBySource(raws), nil
}

// mergeBySource picks the winning source (Apple Watch > RingConn >
// highest-total-hours), filters to that source, and merges contiguous /
// overlapping segments.
func mergeBySource(raws []rawSleepSegment) []sleepSegment {
	if len(raws) == 0 {
		return nil
	}
	totals := map[string]float64{}
	for _, r := range raws {
		totals[r.Source] += r.End.Sub(r.Start).Hours()
	}
	winner := pickWinningSource(totals)
	if winner == "" {
		return nil
	}
	filtered := make([]sleepSegment, 0, len(raws))
	for _, r := range raws {
		if r.Source != winner {
			continue
		}
		filtered = append(filtered, sleepSegment{Start: r.Start, End: r.End})
	}
	return mergeSegments(filtered, mergeTolerance)
}

// pickWinningSource: Apple Watch family > RingConn > highest-total. The
// 1.0h floor on watch-family avoids picking a wisp of Apple Watch data
// over a real night from another source, mirroring the rule in
// sleepCrossValidationPickExpr (see CLAUDE.md "Sleep Source
// Cross-Validation").
func pickWinningSource(totals map[string]float64) string {
	if len(totals) == 0 {
		return ""
	}
	var watch string
	var watchHours float64
	for src, h := range totals {
		if (strings.Contains(src, "Ultra") || strings.Contains(src, "Apple Watch")) && h > 1.0 && h > watchHours {
			watch = src
			watchHours = h
		}
	}
	if watch != "" {
		return watch
	}
	var ring string
	var ringHours float64
	for src, h := range totals {
		if strings.Contains(src, "RingConn") && h > 1.0 && h > ringHours {
			ring = src
			ringHours = h
		}
	}
	if ring != "" {
		return ring
	}
	var fallback string
	var fallbackHours float64
	for src, h := range totals {
		if h > fallbackHours {
			fallbackHours = h
			fallback = src
		}
	}
	return fallback
}

// mergeSegments merges sorted-or-unsorted segments where consecutive
// intervals overlap or sit within `tolerance` of each other.
func mergeSegments(segs []sleepSegment, tolerance time.Duration) []sleepSegment {
	if len(segs) == 0 {
		return nil
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].Start.Before(segs[j].Start) })
	out := make([]sleepSegment, 0, len(segs))
	cur := segs[0]
	for i := 1; i < len(segs); i++ {
		nxt := segs[i]
		if nxt.Start.Sub(cur.End) <= tolerance {
			if nxt.End.After(cur.End) {
				cur.End = nxt.End
			}
			continue
		}
		out = append(out, cur)
		cur = nxt
	}
	out = append(out, cur)
	return out
}

// pickMainSleep returns the merged run whose midpoint is closest to
// refMidnight AND within ±midnightWindow of it. Among those that
// qualify, longest wins; on a duration tie, the later-ending run wins
// (spec edge case for equal-split nights).
func pickMainSleep(segs []sleepSegment, refMidnight time.Time) (sleepSegment, bool) {
	var best sleepSegment
	var bestDur time.Duration
	found := false
	for _, sg := range segs {
		diff := sg.Midpoint().Sub(refMidnight)
		if diff < -midnightWindow || diff > midnightWindow {
			continue
		}
		dur := sg.Duration()
		switch {
		case !found:
			best, bestDur, found = sg, dur, true
		case dur > bestDur:
			best, bestDur = sg, dur
		case dur == bestDur && sg.End.After(best.End):
			best = sg
		}
	}
	return best, found
}

// hourOfDay returns the local time-of-day as a fractional hour (e.g.
// 07:30 → 7.5). Always in [0, 24).
func hourOfDay(t time.Time) float64 {
	return float64(t.Hour()) + float64(t.Minute())/60.0 + float64(t.Second())/3600.0
}
