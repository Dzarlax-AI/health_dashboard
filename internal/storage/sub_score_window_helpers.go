// Package storage — shared trailing-window helpers for sub-score writers.
//
// Recovery Stability and Passive Efficiency compute the same shape of
// rolling stats over a per-date eligible series. These helpers factor
// the math behind a small callback so both writers (and any future
// writer that follows the same shape) share one implementation rather
// than copy-pasting epoch-clipped windowing logic.
//
// The callback contract: `lookup(date)` returns the value to fold into
// the window plus an `eligible` flag. Ineligible or missing dates are
// skipped — they neither contribute to the sum nor displace older
// observations from the window. This matches the writer-level rule
// that ineligible nights/days do not bias the baseline.

package storage

import "time"

// DailyValueLookup returns the value to include in a window for a given
// date and whether the date is eligible. Lookups for absent dates
// should return (nil, false). Lookups for present-but-ineligible dates
// should also return (nil, false) — callers do not distinguish the two
// from a windowing standpoint.
type DailyValueLookup func(date string) (value *float64, eligible bool)

// windowMean returns the arithmetic mean of eligible values over a
// trailing window of `windowDays` ending at `t` (inclusive). When
// `epochStart` is non-empty, dates strictly older than `epochStart`
// are excluded so baselines reset at source-epoch boundaries
// (READINESS_REDESIGN_PLAN.md §3.4). Returns nil when no eligible
// observation lies in the window.
func windowMean(t time.Time, windowDays int, epochStart string, lookup DailyValueLookup) (*float64, int) {
	var sum float64
	var n int
	for i := range windowDays {
		d := t.AddDate(0, 0, -i).Format(isoDate)
		if epochStart != "" && d < epochStart {
			continue
		}
		v, ok := lookup(d)
		if !ok || v == nil {
			continue
		}
		sum += *v
		n++
	}
	if n == 0 {
		return nil, 0
	}
	mean := sum / float64(n)
	return &mean, n
}

// windowEWMA returns the exponentially-weighted moving average over the
// eligible series up to date `t`, with effective window N (smoothing
// factor α = 2/(N+1)). Walks chronologically oldest → newest so the
// most recent observation carries the largest weight. The lookback
// span is `3 × windowN` (capped at ≥90 days) which covers ≥95% of the
// EWMA mass; older history is invisible to this baseline by design.
//
// Like windowMean, dates older than `epochStart` (when set) are
// skipped. Returns nil when no eligible observation falls in the span.
func windowEWMA(t time.Time, windowN int, epochStart string, lookup DailyValueLookup) (*float64, int) {
	alpha := 2.0 / (float64(windowN) + 1.0)
	maxLookback := max(windowN*3, 90)
	start := t.AddDate(0, 0, -maxLookback)
	var ewma float64
	var initialised bool
	var n int
	for d := start; !d.After(t); d = d.AddDate(0, 0, 1) {
		ds := d.Format(isoDate)
		if epochStart != "" && ds < epochStart {
			continue
		}
		v, ok := lookup(ds)
		if !ok || v == nil {
			continue
		}
		if !initialised {
			ewma = *v
			initialised = true
		} else {
			ewma = alpha*(*v) + (1-alpha)*ewma
		}
		n++
	}
	if !initialised {
		return nil, 0
	}
	return &ewma, n
}
