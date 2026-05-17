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

import (
	"math"
	"time"
)

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

// windowStatsBefore returns the arithmetic mean and sample standard
// deviation of eligible values over a trailing window ending the day
// BEFORE `d` (exclusive). Used by event-label classifiers (Acute Risk)
// where the label for day `d` must be measured against history known
// strictly before `d` — including `d` itself in the baseline would let
// the candidate value bias its own threshold.
//
// Walks i = 1..windowDays and looks at `d - i`. The reference day `d`
// is never read. Returns (nil, nil, n) when fewer than 2 eligible
// observations fall in the window (sample SD undefined).
func windowStatsBefore(d time.Time, windowDays int, epochStart string, lookup DailyValueLookup) (mean, sd *float64, n int) {
	var sum, sumSq float64
	for i := 1; i <= windowDays; i++ {
		ds := d.AddDate(0, 0, -i).Format(isoDate)
		if epochStart != "" && ds < epochStart {
			continue
		}
		v, ok := lookup(ds)
		if !ok || v == nil {
			continue
		}
		sum += *v
		sumSq += (*v) * (*v)
		n++
	}
	if n < 2 {
		return nil, nil, n
	}
	m := sum / float64(n)
	// Sample variance: (Σx² − n·μ²) / (n − 1). Clamp at 0 for FP safety
	// when the population is effectively constant.
	variance := (sumSq - float64(n)*m*m) / float64(n-1)
	if variance < 0 {
		variance = 0
	}
	s := math.Sqrt(variance)
	return &m, &s, n
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
	maxLookback := ewmaLookbackDays(windowN)
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

// ewmaLookbackDays — actual span (in calendar days strictly before
// `t`-inclusive) walked by windowEWMA for the given effective window N.
// Centralised so callers wanting to detect whether the EWMA touches a
// source-epoch boundary don't have to re-derive the formula and get the
// reason wrong (which is what happened in the initial cut of
// classifyBaselineNullReason).
//
// Definition matches the loop in windowEWMA:
//
//	maxLookback := max(windowN*3, 90)
//	start := t.AddDate(0, 0, -maxLookback)
//	for d := start; !d.After(t); ...
//
// So earliest considered calendar day = `t - ewmaLookbackDays(N)`.
func ewmaLookbackDays(windowN int) int {
	if windowN*3 > 90 {
		return windowN * 3
	}
	return 90
}

// classifyBaselineNullReason picks the chip-facing reason enum for a
// nil baseline value. Centralised so every build*NaiveBaselines call
// site applies the same rule.
//
// `earliestOffsetDays` is **how many calendar days before `t` the
// baseline's lookback reaches** — i.e. the earliest day considered is
// `t - earliestOffsetDays`. Picking this number per baseline kind is
// the caller's responsibility because each helper walks a different
// span:
//
//   - persistence_yesterday: 0 (looks only at `t` itself)
//   - rolling_7d_mean → windowMean(t, 7): 6  (window [t-6..t])
//   - rolling_30d_mean → windowMean(t, 30): 29
//   - ewma_45d → windowEWMA(t, 45): ewmaLookbackDays(45) = 135
//     (NOT 44 — windowEWMA reaches back max(N*3, 90) days, not N)
//   - event_base_rate → priorEventBaseRate(t, 90): 90
//     (the helper walks i=1..90 strictly before `t`, earliest = t-90)
//
// Rule:
//   - If `epochStart` is set AND the earliest considered calendar day
//     falls before `epochStart`, the window is clipped by the current
//     source_epoch → `BaselineReasonSourceEpochBoundary`. Operator
//     intervention (epoch catalogue) shaped this, not just time-since-
//     onboarding.
//   - Otherwise the window lies fully inside the epoch but has no
//     eligible observations yet → `BaselineReasonWarmup`.
func classifyBaselineNullReason(t time.Time, earliestOffsetDays int, epochStart string) string {
	if epochStart == "" {
		return BaselineReasonWarmup
	}
	if earliestOffsetDays < 0 {
		earliestOffsetDays = 0
	}
	windowStart := t.AddDate(0, 0, -earliestOffsetDays).Format(isoDate)
	if windowStart < epochStart {
		return BaselineReasonSourceEpochBoundary
	}
	return BaselineReasonWarmup
}
