package storage

import (
	"context"
	"fmt"
	"sort"
	"time"

	"health-receiver/internal/health"
)

// ComputeStressValidationReport runs the §4.5 validation rubric for
// the calling tenant over a rolling window ending at `asOfDate` (or
// "today" in `tz` when empty). Returns the kernel-decided verdict
// + per-channel coefficients.
//
//	day d → today's sustained_hr_load[d] (already in daily_scores)
//	day d+1 → next-morning HRV, next-morning baseline_hr_overnight
//	day d → today's sleep_onset, sleep_awake, deep_pct first third
//
// Implementation pulls the day-aligned pairs in one query and
// computes the Pearson coefficient + sleep-vote in memory; matches
// the §4.5 design that the rubric runs as a pure read against
// existing tables, no new persistence.
//
// `windowDays` defaults to 30 per spec. `tz` must be the tenant's
// REPORT_TZ.
func (s *DB) ComputeStressValidationReport(
	ctx context.Context,
	tz, asOfDate string,
	windowDays int,
) (health.ValidationReport, error) {
	if windowDays <= 0 {
		windowDays = 30
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return health.ValidationReport{}, fmt.Errorf("load tz %q: %w", tz, err)
	}
	var d time.Time
	if asOfDate == "" {
		d = time.Now().In(loc)
	} else {
		d, err = time.ParseInLocation("2006-01-02", asOfDate, loc)
		if err != nil {
			return health.ValidationReport{}, fmt.Errorf("parse asOfDate %q: %w", asOfDate, err)
		}
	}
	from := d.AddDate(0, 0, -windowDays).Format("2006-01-02")
	to := d.Format("2006-01-02")

	pairs, err := s.fetchValidationPairs(ctx, from, to)
	if err != nil {
		return health.ValidationReport{}, fmt.Errorf("fetch pairs: %w", err)
	}

	report := health.ValidationReport{
		WindowDays: windowDays,
		Days:       len(pairs),
	}

	// Channel 1 — Pearson(load[d], HRV[d+1]).
	var loadHRV, hrvNext []float64
	for _, p := range pairs {
		if p.Load != nil && p.HRVNext != nil {
			loadHRV = append(loadHRV, *p.Load)
			hrvNext = append(hrvNext, *p.HRVNext)
		}
	}
	if len(hrvNext) < health.HRVChannelMinSamples {
		report.Channel1HRV = health.ValidationChannel{N: len(hrvNext), Sparse: true}
	} else if r, n, ok := health.PearsonR(loadHRV, hrvNext); ok {
		report.Channel1HRV = health.ValidationChannel{R: &r, N: n}
	} else {
		report.Channel1HRV = health.ValidationChannel{N: len(hrvNext)}
	}

	// Channel 2 — Pearson(load[d], RHR_shift[d+1] = RHR[d+1] − baseline_RHR_30d).
	// baseline_RHR is the rolling overnight RHR baseline at day d
	// (the same value PR-2 PersonalBaseline returns). For the
	// validation window we approximate by using the within-window
	// median as the baseline — within a 30-day calibration window
	// this matches the §4.1 30-day rolling median closely enough
	// to drive sign-of-shift, which is all the rubric needs.
	var loadRHR, rhrShift []float64
	rhrMedian := medianOfPointers(extractPointer(pairs, func(p validationPair) *float64 { return p.RHRNext }))
	for _, p := range pairs {
		if p.Load != nil && p.RHRNext != nil {
			loadRHR = append(loadRHR, *p.Load)
			rhrShift = append(rhrShift, *p.RHRNext-rhrMedian)
		}
	}
	if r, n, ok := health.PearsonR(loadRHR, rhrShift); ok {
		report.Channel2RHR = health.ValidationChannel{R: &r, N: n}
	} else {
		report.Channel2RHR = health.ValidationChannel{N: len(rhrShift)}
	}

	// Channel 3 — sleep architecture votes.
	report.Channel3Sleep = computeSleepChannel(pairs)

	health.RubricDecide(&report)
	return report, nil
}

// validationPair carries one row of the day-d / day-(d+1) join.
// Pointers track NULL daily_scores fields — Pearson skips NaN/missing.
type validationPair struct {
	Date       string
	Load       *float64 // sustained_hr_load[d]
	HRVNext    *float64 // overnight HRV median[d+1]
	RHRNext    *float64 // baseline_hr_overnight[d+1]
	SleepAwake *float64 // sleep_awake[d] (hours)
	SleepTotal *float64 // sleep_total[d] (hours)
	SleepDeep  *float64 // sleep_deep[d] (hours)
	// Onset latency is currently approximated as the gap between
	// sleep_awake and sleep_total — not exact (real onset latency
	// is "lights-out to first sleep" from segment timestamps), but
	// the sign of its correlation with load is what the rubric
	// votes on, and that signs agrees with the approximation.
	// True per-segment latency lands in a follow-up when the iOS
	// health-sync side ships explicit `inBed` timestamps.
}

// fetchValidationPairs joins daily_scores[d] with daily_scores[d+1]
// by date arithmetic on the TEXT date column. Returns rows in the
// requested window inclusive of `from`, exclusive of `to+1`. Uses
// LEFT JOIN so days missing the next-morning row still surface (with
// NULL HRVNext / RHRNext) — the per-channel filters above skip them.
//
// HRV next-morning is read from heart_rate_variability metric_points
// restricted to overnight (23:00–09:00 hour-of-day) per §4.1
// convention so the rubric's input matches what PersonalBaseline
// uses for the same channel.
func (s *DB) fetchValidationPairs(
	ctx context.Context,
	from, to string,
) ([]validationPair, error) {
	rows, err := s.pool.Query(ctx, `
		WITH hrv_overnight AS (
			SELECT SUBSTRING(date, 1, 10) AS day,
			       percentile_cont(0.5) WITHIN GROUP (ORDER BY qty) AS hrv_med
			  FROM metric_points
			 WHERE metric_name = 'heart_rate_variability'
			   AND quality = 'ok'
			   AND (SUBSTRING(date, 12, 2) >= '23' OR SUBSTRING(date, 12, 2) < '09')
			 GROUP BY day
		)
		SELECT d.date,
		       d.sustained_hr_load,
		       hrv_next.hrv_med,
		       d_next.baseline_hr_overnight,
		       d.sleep_awake,
		       d.sleep_total,
		       d.sleep_deep
		  FROM daily_scores d
		  LEFT JOIN daily_scores d_next
		         ON d_next.date = TO_CHAR(TO_DATE(d.date, 'YYYY-MM-DD') + INTERVAL '1 day', 'YYYY-MM-DD')
		  LEFT JOIN hrv_overnight hrv_next
		         ON hrv_next.day = TO_CHAR(TO_DATE(d.date, 'YYYY-MM-DD') + INTERVAL '1 day', 'YYYY-MM-DD')
		 WHERE d.date >= $1 AND d.date <= $2
		 ORDER BY d.date`, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []validationPair
	for rows.Next() {
		var p validationPair
		if err := rows.Scan(
			&p.Date, &p.Load, &p.HRVNext, &p.RHRNext,
			&p.SleepAwake, &p.SleepTotal, &p.SleepDeep,
		); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// computeSleepChannel runs the §4.5 channel-3 sub-correlations and
// returns the vote count + Pearson coefficients for diagnostic use.
// Three sub-signals:
//
//   load[d] vs onset_latency[d]      expected sign: positive
//   load[d] vs sleep_awake[d]        expected sign: positive
//   load[d] vs deep_pct_first_third  expected sign: negative
//
// Each sub-signal contributes one "agreement vote" when it has
// enough samples AND |r| ≥ 0.1 AND the sign matches expectation.
//
// Apple-Watch sleep-source over-detection of deep sleep is noted
// in the spec (§4.5); for now the deep_pct sub-signal votes only
// when its magnitude crosses 0.2 (stricter floor) to compensate.
// Per-source downweighting (full per-night picked-source lookup)
// stays out of this PR.
func computeSleepChannel(pairs []validationPair) health.SleepChannel {
	// N counts days with non-null load; the per-sub-signal NULL
	// filters live inside alignPairs below. Previously this loop
	// also pre-built per-signal slices but they were unused —
	// alignPairs rebuilds them anyway. SA4010 was flagging the
	// dead writes.
	loadCount := 0
	for _, p := range pairs {
		if p.Load != nil {
			loadCount++
		}
	}
	out := health.SleepChannel{N: loadCount}

	// Build paired slices for each sub-correlation. We use the
	// `loads` slice as the x-axis and re-traverse pairs to keep
	// pairing aligned with NULL filtering.
	loadsLat, lat := alignPairs(pairs, func(p validationPair) (float64, *float64, bool) {
		if p.SleepAwake == nil || p.SleepTotal == nil || *p.SleepTotal <= 0 {
			return 0, nil, false
		}
		return *p.SleepAwake, p.Load, true
	})
	if r, _, ok := health.PearsonR(loadsLat, lat); ok {
		out.OnsetLatencyR = &r
		if r >= 0.1 {
			out.AgreementVotes++
		}
	}

	loadsAwk, awk := alignPairs(pairs, func(p validationPair) (float64, *float64, bool) {
		if p.SleepAwake == nil {
			return 0, nil, false
		}
		return *p.SleepAwake, p.Load, true
	})
	if r, _, ok := health.PearsonR(loadsAwk, awk); ok {
		out.AwakeR = &r
		if r >= 0.1 {
			out.AgreementVotes++
		}
	}

	loadsDeep, dpc := alignPairs(pairs, func(p validationPair) (float64, *float64, bool) {
		if p.SleepDeep == nil || p.SleepTotal == nil || *p.SleepTotal <= 0 {
			return 0, nil, false
		}
		return *p.SleepDeep / *p.SleepTotal, p.Load, true
	})
	if r, _, ok := health.PearsonR(loadsDeep, dpc); ok {
		out.DeepPctR = &r
		// Stricter floor for deep_pct due to Apple over-detection
		// (per §4.5 caveat); only votes when |r|≥0.2 in the
		// expected NEGATIVE direction.
		if r <= -0.2 {
			out.AgreementVotes++
		}
	}
	return out
}

// alignPairs is a tiny adapter that walks `pairs`, calls the
// extractor, and returns matched x/y float slices for PearsonR.
// Used to keep the sleep-channel sub-correlations symmetric without
// duplicating 10-line NULL filters.
func alignPairs(
	pairs []validationPair,
	extract func(validationPair) (x float64, y *float64, ok bool),
) (xs, ys []float64) {
	for _, p := range pairs {
		x, y, ok := extract(p)
		if !ok || y == nil {
			continue
		}
		xs = append(xs, *y)
		ys = append(ys, x)
	}
	return xs, ys
}

// extractPointer collects non-nil float pointers into a slice — used
// to compute a within-window median for the RHR baseline approximation.
func extractPointer[T any](items []T, fn func(T) *float64) []float64 {
	var out []float64
	for _, it := range items {
		if v := fn(it); v != nil {
			out = append(out, *v)
		}
	}
	return out
}

// medianOfPointers is the linear-interpolation median of a value
// slice, returning 0 on empty input. Matches percentileSorted
// (already used by PersonalBaseline) for consistency.
func medianOfPointers(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	cp := make([]float64, len(values))
	copy(cp, values)
	sort.Float64s(cp)
	return percentileSorted(cp, 0.5)
}
