package storage

import (
	"math"
	"testing"
	"time"
)

// staticLookup builds a DailyValueLookup over a small in-memory map
// for tests that don't need the AutonomicRow/SleepRow plumbing.
func staticLookup(values map[string]float64) DailyValueLookup {
	return func(date string) (*float64, bool) {
		v, ok := values[date]
		if !ok {
			return nil, false
		}
		return &v, true
	}
}

func TestWindowStatsBefore_ExcludesAnchorDay(t *testing.T) {
	// Build a series where day d itself is a clear outlier (100) and
	// the prior 5 days are 10..14. The mean+sd we get back must NOT
	// reflect the outlier — the anchor day must be excluded.
	d := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	values := map[string]float64{
		"2026-05-15": 100, // anchor; must be excluded
		"2026-05-14": 14,
		"2026-05-13": 13,
		"2026-05-12": 12,
		"2026-05-11": 11,
		"2026-05-10": 10,
	}
	mean, sd, n := windowStatsBefore(d, 7, "", staticLookup(values))
	if n != 5 {
		t.Errorf("n = %d, want 5 (anchor excluded, 5 prior days)", n)
	}
	wantMean := (10.0 + 11.0 + 12.0 + 13.0 + 14.0) / 5.0
	if mean == nil || math.Abs(*mean-wantMean) > 1e-9 {
		t.Errorf("mean = %v, want %v", mean, wantMean)
	}
	if sd == nil || *sd >= 5 {
		t.Errorf("sd = %v, expected ~1.58 (sample SD of 10..14), got something inflated by anchor", sd)
	}
}

func TestWindowStatsBefore_EpochClipping(t *testing.T) {
	d := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	values := map[string]float64{
		"2026-05-10": 100, // before epoch start; must be excluded
		"2026-05-12": 20,
		"2026-05-13": 22,
		"2026-05-14": 24,
	}
	mean, _, n := windowStatsBefore(d, 7, "2026-05-12", staticLookup(values))
	if n != 3 {
		t.Errorf("n = %d, want 3 (epoch clip removes 2026-05-10)", n)
	}
	wantMean := 22.0 // (20+22+24)/3
	if mean == nil || math.Abs(*mean-wantMean) > 1e-9 {
		t.Errorf("mean = %v, want %v", mean, wantMean)
	}
}

func TestWindowStatsBefore_NotEnoughObservations(t *testing.T) {
	d := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	values := map[string]float64{"2026-05-14": 20} // only 1 prior day
	mean, sd, n := windowStatsBefore(d, 7, "", staticLookup(values))
	if n != 1 {
		t.Errorf("n = %d, want 1", n)
	}
	if mean != nil || sd != nil {
		t.Errorf("expected nil mean/sd with n<2, got mean=%v sd=%v", mean, sd)
	}
}

func TestWindowStatsBefore_ConstantSeriesProducesZeroSD(t *testing.T) {
	d := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	values := map[string]float64{
		"2026-05-14": 50, "2026-05-13": 50, "2026-05-12": 50, "2026-05-11": 50,
	}
	mean, sd, n := windowStatsBefore(d, 7, "", staticLookup(values))
	if n != 4 {
		t.Errorf("n = %d, want 4", n)
	}
	if mean == nil || *mean != 50 {
		t.Errorf("mean = %v, want 50", mean)
	}
	if sd == nil || *sd != 0 {
		t.Errorf("sd = %v, want 0 (constant series)", sd)
	}
}

func TestWindowStatsBefore_SkipsAbsentAndIneligibleDates(t *testing.T) {
	d := time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC)
	// 7-day window: only 3 dates present. Gaps don't break the math.
	values := map[string]float64{
		"2026-05-14": 30,
		"2026-05-11": 32,
		"2026-05-09": 34,
	}
	_, _, n := windowStatsBefore(d, 7, "", staticLookup(values))
	if n != 3 {
		t.Errorf("n = %d, want 3 (gaps in window are skipped, not zero-filled)", n)
	}
}
