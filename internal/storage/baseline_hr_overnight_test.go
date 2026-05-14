package storage

import (
	"math"
	"testing"
	"time"
)

func TestResolveBaselineWindow_NormalWake(t *testing.T) {
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	// Wake at 07:30. Window should be [04:30, 07:30) on date d.
	start, end, imp := resolveBaselineWindow(d, 7.5, false, true, loc)
	if imp {
		t.Fatal("happy path must not be imputed")
	}
	wantStart := time.Date(2026, 5, 13, 4, 30, 0, 0, loc)
	wantEnd := time.Date(2026, 5, 13, 7, 30, 0, 0, loc)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("window = [%v, %v), want [%v, %v)", start, end, wantStart, wantEnd)
	}
}

func TestResolveBaselineWindow_LateWake(t *testing.T) {
	// Sleep until ~10:00 (late riser / weekend). Last 3h = [07:00, 10:00).
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	start, end, imp := resolveBaselineWindow(d, 10.0, false, true, loc)
	if imp {
		t.Fatal("late wake is real data, not imputed")
	}
	if start.Hour() != 7 || end.Hour() != 10 {
		t.Fatalf("late-wake window = [%v, %v), want [07:00, 10:00)", start, end)
	}
}

func TestResolveBaselineWindow_EarlyWake(t *testing.T) {
	// Wake at 05:15. Window = [02:15, 05:15) — earlier than the 03:00–06:00
	// fallback, which is the whole point of the per-date resolver.
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	start, end, imp := resolveBaselineWindow(d, 5.25, false, true, loc)
	if imp {
		t.Fatal("early wake is real data, not imputed")
	}
	wantStart := time.Date(2026, 5, 13, 2, 15, 0, 0, loc)
	wantEnd := time.Date(2026, 5, 13, 5, 15, 0, 0, loc)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("early-wake window = [%v, %v), want [%v, %v)", start, end, wantStart, wantEnd)
	}
}

func TestResolveBaselineWindow_ImputedFallback(t *testing.T) {
	// wakeImputed=true OR wakeOK=false both should return the
	// fixed 03:00–06:00 fallback with imputed=true.
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	cases := []struct {
		name        string
		wakeHour    float64
		wakeImputed bool
		wakeOK      bool
	}{
		{"resolver imputed", 7.0, true, true},
		{"resolver failed", 0.0, false, false},
		{"resolver both", 0.0, true, false},
	}
	wantStart := time.Date(2026, 5, 13, 3, 0, 0, 0, loc)
	wantEnd := time.Date(2026, 5, 13, 6, 0, 0, 0, loc)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			start, end, imp := resolveBaselineWindow(d, c.wakeHour, c.wakeImputed, c.wakeOK, loc)
			if !imp {
				t.Fatal("expected imputed=true on fallback path")
			}
			if !start.Equal(wantStart) || !end.Equal(wantEnd) {
				t.Fatalf("fallback window = [%v, %v), want [%v, %v)", start, end, wantStart, wantEnd)
			}
		})
	}
}

func TestResolveBaselineWindow_TZAware(t *testing.T) {
	// The window MUST be expressed in the tenant's local TZ so SQL
	// comparison against metric_points.date (TEXT in ±TZ format)
	// picks up the right samples. Wake at 07:00 local in
	// Europe/Belgrade → window crosses the UTC-vs-local boundary
	// silently, so the wall-clock hour, not the UTC hour, is what
	// we pin here.
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Skip("Europe/Belgrade tzdata unavailable")
	}
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	start, end, imp := resolveBaselineWindow(d, 7.0, false, true, loc)
	if imp {
		t.Fatal("happy path must not be imputed")
	}
	if start.Hour() != 4 || end.Hour() != 7 {
		t.Fatalf("Belgrade window = [%v, %v), want [04:00, 07:00) local", start, end)
	}
	// Verify the TZ offset is preserved (i.e. start/end carry the
	// loc, not UTC). Bug-bait check — `time.Date(..., loc)` returns
	// a time *in* that location; if a future refactor accidentally
	// converted via `.UTC()` the offset would flip to 0.
	_, soff := start.Zone()
	_, eoff := end.Zone()
	if soff == 0 || eoff == 0 {
		t.Fatalf("window dropped tz offset: start=%v end=%v", start, end)
	}
}

func TestIsFiniteFloat(t *testing.T) {
	// Inf/NaN built via math package — constant-folded 1e309 and 0/0
	// trip the compiler. Same intent: pin the guard's edges.
	posInf := math.Inf(1)
	negInf := math.Inf(-1)
	nan := math.NaN()
	cases := []struct {
		v    float64
		want bool
	}{
		{0, true},
		{42.5, true},
		{-1, true},
		{1e300, true},
		{posInf, false},
		{negInf, false},
		{nan, false},
	}
	for _, c := range cases {
		if got := isFiniteFloat(c.v); got != c.want {
			t.Errorf("isFiniteFloat(%v) = %v, want %v", c.v, got, c.want)
		}
	}
}
