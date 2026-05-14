package storage

import (
	"testing"
	"time"
)

func TestResolveAwakeBounds_NormalDay(t *testing.T) {
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	// Wake 07:30, onset 22:30 — typical day, contiguous within d.
	start, end := resolveAwakeBounds(d, 7.5, 22.5, loc)
	wantStart := time.Date(2026, 5, 13, 7, 30, 0, 0, loc)
	wantEnd := time.Date(2026, 5, 13, 22, 30, 0, 0, loc)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("normal day = [%v, %v), want [%v, %v)", start, end, wantStart, wantEnd)
	}
}

func TestResolveAwakeBounds_CrossMidnight(t *testing.T) {
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	// Wake 08:00, onset 01:30 (i.e. next-day AM). Cross-midnight.
	// Awake window should be [d 08:00, d+1 01:30).
	start, end := resolveAwakeBounds(d, 8.0, 1.5, loc)
	wantStart := time.Date(2026, 5, 13, 8, 0, 0, 0, loc)
	wantEnd := time.Date(2026, 5, 14, 1, 30, 0, 0, loc)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("cross-midnight = [%v, %v), want [%v, %v)", start, end, wantStart, wantEnd)
	}
}

func TestResolveAwakeBounds_ImputedDefault(t *testing.T) {
	// WakeTimeForDate's imputed fallback returns 07:00 / 22:00.
	// Coverage bounds should land at d 07:00 → d 22:00.
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	start, end := resolveAwakeBounds(d, 7.0, 22.0, loc)
	wantStart := time.Date(2026, 5, 13, 7, 0, 0, 0, loc)
	wantEnd := time.Date(2026, 5, 13, 22, 0, 0, 0, loc)
	if !start.Equal(wantStart) || !end.Equal(wantEnd) {
		t.Fatalf("imputed = [%v, %v), want [%v, %v)", start, end, wantStart, wantEnd)
	}
}

func TestResolveAwakeBounds_EqualHours(t *testing.T) {
	// Pathological: wake == onset (zero-width awake window). The
	// cross-midnight branch fires on `<` so this stays same-day,
	// producing an empty interval. SQL count over empty range is
	// 0; HRCoverageHours then reports 0 hours, which correctly
	// gates stress drain off for this nonsense day.
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	start, end := resolveAwakeBounds(d, 10.0, 10.0, loc)
	if !start.Equal(end) {
		t.Fatalf("equal-hours window should collapse: [%v, %v)", start, end)
	}
}

func TestResolveAwakeBounds_TZAware(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Belgrade")
	if err != nil {
		t.Skip("Europe/Belgrade tzdata unavailable")
	}
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	start, end := resolveAwakeBounds(d, 7.5, 23.0, loc)
	if start.Hour() != 7 || start.Minute() != 30 {
		t.Fatalf("Belgrade start = %v, want 07:30 local", start)
	}
	if end.Hour() != 23 || end.Minute() != 0 {
		t.Fatalf("Belgrade end = %v, want 23:00 local", end)
	}
	// TZ offset must be preserved (regression guard against accidental
	// .UTC() conversion in a future refactor).
	_, soff := start.Zone()
	_, eoff := end.Zone()
	if soff == 0 || eoff == 0 {
		t.Fatalf("dropped tz offset: start=%v end=%v", start, end)
	}
}

func TestSplitHour(t *testing.T) {
	cases := []struct {
		in       float64
		wantH    int
		wantM    int
	}{
		{0.0, 0, 0},
		{7.5, 7, 30},
		{23.99, 23, 59},
		{12.25, 12, 15},
		// Defensive clamps: negative / overflow stay in [0, 23] / [0, 59]
		{-1.0, 0, 0},
		{25.0, 23, 0},
	}
	for _, c := range cases {
		gotH, gotM := splitHour(c.in)
		if gotH != c.wantH || gotM != c.wantM {
			t.Errorf("splitHour(%v) = (%d, %d), want (%d, %d)", c.in, gotH, gotM, c.wantH, c.wantM)
		}
	}
}
