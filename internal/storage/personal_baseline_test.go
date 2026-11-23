package storage

import (
	"math"
	"sort"
	"testing"
	"time"
)

func TestClassifyState_Cold(t *testing.T) {
	now := time.Now()
	cases := []int{0, 1, 2}
	for _, n := range cases {
		if got := classifyState(n, now, now); got != CalibrationCold {
			t.Errorf("classifyState(%d) = %q, want cold", n, got)
		}
	}
}

func TestClassifyState_Warmup(t *testing.T) {
	now := time.Now()
	cases := []int{3, 4, 5, 6}
	for _, n := range cases {
		if got := classifyState(n, now, now); got != CalibrationWarmup {
			t.Errorf("classifyState(%d, fresh) = %q, want warmup", n, got)
		}
	}
}

func TestClassifyState_Steady(t *testing.T) {
	now := time.Now()
	// 7+ samples, newest 0-13 days old → steady
	for _, ageDays := range []int{0, 1, 7, 13} {
		newest := now.AddDate(0, 0, -ageDays)
		if got := classifyState(7, newest, now); got != CalibrationSteady {
			t.Errorf("classifyState(7, %dd old) = %q, want steady", ageDays, got)
		}
	}
}

func TestClassifyState_StaleDemoted(t *testing.T) {
	// 7+ samples but newest > 14 days old → demoted to warmup
	// (Apple Watch off for weeks; baseline values exist but stale).
	now := time.Now()
	stale := now.AddDate(0, 0, -15)
	if got := classifyState(20, stale, now); got != CalibrationWarmup {
		t.Errorf("classifyState(20, 15d old) = %q, want warmup (stale demote)", got)
	}
}

func TestClassifyState_ZeroNewestSafe(t *testing.T) {
	// Defensive: sampleCount ≥ 7 but newest is zero Time
	// (shouldn't happen in practice but guard against panic).
	now := time.Now()
	if got := classifyState(10, time.Time{}, now); got != CalibrationWarmup {
		t.Errorf("classifyState(10, zero) = %q, want warmup (safe default)", got)
	}
}

func TestComputeMedianMADSD_Empty(t *testing.T) {
	median, sd := computeMedianMADSD(nil)
	if median != 0 || sd != 0 {
		t.Errorf("empty = (%v, %v), want (0, 0)", median, sd)
	}
}

func TestComputeMedianMADSD_Single(t *testing.T) {
	median, sd := computeMedianMADSD([]float64{42})
	if median != 42 || sd != 0 {
		t.Errorf("single = (%v, %v), want (42, 0)", median, sd)
	}
}

func TestComputeMedianMADSD_OddCount(t *testing.T) {
	// Wikipedia worked example: [1, 1, 2, 2, 4, 6, 9]
	// median = 2
	// |x - median| = [1, 1, 0, 0, 2, 4, 7]
	// sorted: [0, 0, 1, 1, 2, 4, 7] — median = 1
	// SD = 1.4826 × 1 = 1.4826
	median, sd := computeMedianMADSD([]float64{1, 1, 2, 2, 4, 6, 9})
	if math.Abs(median-2) > 1e-9 {
		t.Errorf("median = %v, want 2", median)
	}
	if math.Abs(sd-1.4826) > 1e-9 {
		t.Errorf("sd = %v, want 1.4826", sd)
	}
}

func TestComputeMedianMADSD_EvenCount(t *testing.T) {
	// Even count → median is linear interpolation between middle pair.
	// [1, 2, 3, 4] → median = (2+3)/2 = 2.5
	// |x - 2.5| = [1.5, 0.5, 0.5, 1.5]
	// sorted: [0.5, 0.5, 1.5, 1.5] → mad = (0.5+1.5)/2 = 1.0
	// SD = 1.4826
	median, sd := computeMedianMADSD([]float64{1, 2, 3, 4})
	if math.Abs(median-2.5) > 1e-9 {
		t.Errorf("median = %v, want 2.5", median)
	}
	if math.Abs(sd-1.4826) > 1e-9 {
		t.Errorf("sd = %v, want 1.4826", sd)
	}
}

func TestComputeMedianMADSD_RobustToOutliers(t *testing.T) {
	// MAD is robust to outliers. 9 normal HR samples ~ 60 bpm plus
	// one outlier 120 (e.g. forgot to stop workout):
	// median ≈ 60, MAD picks middle absolute deviation, outlier
	// can't drag it.
	samples := []float64{58, 59, 60, 60, 60, 61, 61, 62, 63, 120}
	median, sd := computeMedianMADSD(samples)
	if math.Abs(median-60.5) > 1e-9 {
		t.Errorf("median = %v, want 60.5 (outlier-resistant)", median)
	}
	// SD should be small despite the outlier — proves robust property.
	if sd > 3.0 {
		t.Errorf("sd = %v, expected ≤ 3 with 1 outlier", sd)
	}
}

func TestChannelSDFloor_AllChannels(t *testing.T) {
	cases := []struct {
		ch   BaselineChannel
		want float64
	}{
		{ChannelHRAwake, SDFloorHR},
		{ChannelHROvernight, SDFloorHR},
		{ChannelHRV, SDFloorHRV},
		{ChannelResp, SDFloorResp},
		{ChannelTemp, SDFloorTemp},
		{BaselineChannel("unknown"), 0},
	}
	for _, c := range cases {
		if got := channelSDFloor(c.ch); got != c.want {
			t.Errorf("channelSDFloor(%q) = %v, want %v", c.ch, got, c.want)
		}
	}
}

func TestSDFloorConstants_LoadBearing(t *testing.T) {
	// Pin the constants — they tune the §4.1 baseline behaviour
	// and changing them silently shifts z-scores across all
	// history. Any change should be intentional, not accidental
	// (and ideally come with cohort data per §6 Q6).
	if SDFloorHR != 3.0 {
		t.Errorf("SDFloorHR = %v, want 3.0", SDFloorHR)
	}
	if SDFloorHRV != 5.0 {
		t.Errorf("SDFloorHRV = %v, want 5.0", SDFloorHRV)
	}
	if SDFloorResp != 0.5 {
		t.Errorf("SDFloorResp = %v, want 0.5", SDFloorResp)
	}
	if SDFloorTemp != 0.1 {
		t.Errorf("SDFloorTemp = %v, want 0.1", SDFloorTemp)
	}
	if MinTempSamples != 14 {
		t.Errorf("MinTempSamples = %v, want 14", MinTempSamples)
	}
}

func TestPercentileSorted_MatchesPostgresCont(t *testing.T) {
	// Linear interpolation; result should match PostgreSQL's
	// percentile_cont(0.5) on the same input so Go-side and
	// SQL-side medians don't drift.
	cases := []struct {
		in   []float64
		p    float64
		want float64
	}{
		{[]float64{}, 0.5, 0},
		{[]float64{42}, 0.5, 42},
		{[]float64{1, 2, 3, 4}, 0.5, 2.5},
		{[]float64{1, 2, 3, 4, 5}, 0.5, 3},
		{[]float64{10, 20, 30, 40}, 0.25, 17.5},
	}
	for _, c := range cases {
		got := percentileSorted(c.in, c.p)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("percentileSorted(%v, %v) = %v, want %v", c.in, c.p, got, c.want)
		}
	}
}

func TestParseBaselineSampleTS_AllShapes(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Belgrade")
	cases := []struct {
		in   string
		want time.Time
	}{
		// metric_points.date — full timestamp with TZ
		{"2026-05-13 07:30:00 +0200", time.Date(2026, 5, 13, 7, 30, 0, 0, loc)},
		// hourly_metrics.hour — no seconds, no TZ
		{"2026-05-13 07:00", time.Date(2026, 5, 13, 7, 0, 0, 0, loc)},
		// daily_scores.date — date-only
		{"2026-05-13", time.Date(2026, 5, 13, 0, 0, 0, 0, loc)},
		// Garbage → zero time
		{"not a date", time.Time{}},
		{"", time.Time{}},
	}
	for _, c := range cases {
		got := parseBaselineSampleTS(c.in, loc)
		if !got.Equal(c.want) {
			t.Errorf("parseBaselineSampleTS(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestAgeOf_ZeroSafe(t *testing.T) {
	now := time.Now()
	if got := ageOf(time.Time{}, now); got != 0 {
		t.Errorf("ageOf(zero, now) = %v, want 0", got)
	}
	got := ageOf(now.Add(-3*time.Hour), now)
	if got < 3*time.Hour-time.Second || got > 3*time.Hour+time.Second {
		t.Errorf("ageOf(3h-ago) = %v, want ≈3h", got)
	}
}

// classifyState implements the §4.1 state machine:
//
//	cold:    samples < 3
//	warmup:  3 ≤ samples < 7
//	steady:  samples ≥ 7 AND newest ≤ 14 days old
//
// When samples ≥ 7 but newest > 14 days, classify as `warmup` — the
// baseline is technically populated but the latest data point is
// too old to trust as "still describes this person's current
// physiology". (Apple-Watch-off-for-weeks scenario.)
//
// Pure function — pinned by unit tests.
func classifyState(sampleCount int, newest, now time.Time) CalibrationState {
	if sampleCount < 3 {
		return CalibrationCold
	}
	if sampleCount < 7 {
		return CalibrationWarmup
	}
	if newest.IsZero() {
		// Shouldn't happen — sampleCount ≥ 7 implies samples exist
		// — but guard rather than read uninitialized time.
		return CalibrationWarmup
	}
	if now.Sub(newest) > 14*24*time.Hour {
		return CalibrationWarmup
	}
	return CalibrationSteady
}

// computeMedianMADSD returns the median and MAD-based SD of the
// input. MAD = median absolute deviation; SD ≈ 1.4826 × MAD for a
// normally-distributed sample (the constant comes from
// Φ⁻¹(0.75) = 0.6745 → 1/0.6745). Robust to ~25% outliers, which
// is exactly the post-illness / heavy-alcohol / travel-day pattern
// we see in real autonomic data.
//
// Mutates the slice (sort) — caller should pass a slice it doesn't
// need preserved, or copy first.
//
// Pure function — pinned by unit tests against the worked examples
// in https://en.wikipedia.org/wiki/Median_absolute_deviation.
func computeMedianMADSD(samples []float64) (median, sd float64) {
	n := len(samples)
	if n == 0 {
		return 0, 0
	}
	sort.Float64s(samples)
	median = percentileSorted(samples, 0.5)
	devs := make([]float64, n)
	for i, v := range samples {
		devs[i] = math.Abs(v - median)
	}
	sort.Float64s(devs)
	mad := percentileSorted(devs, 0.5)
	sd = 1.4826 * mad
	return median, sd
}

// channelSDFloor returns the SD floor for the given channel. Pure
// function — keeps the floor constants in one switch so changes
// stay coordinated.
func channelSDFloor(channel BaselineChannel) float64 {
	switch channel {
	case ChannelHRAwake, ChannelHROvernight:
		return SDFloorHR
	case ChannelHRV:
		return SDFloorHRV
	case ChannelResp:
		return SDFloorResp
	case ChannelTemp:
		return SDFloorTemp
	default:
		return 0
	}
}

// ageOf returns the age of `newest` relative to `now`. Zero
// duration when `newest` is the zero time (no samples).
func ageOf(newest, now time.Time) time.Duration {
	if newest.IsZero() {
		return 0
	}
	return now.Sub(newest)
}
