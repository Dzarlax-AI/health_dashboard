package storage

import (
	"math"
	"testing"
	"time"
)

// seg builds a sleepSegment from a base date, start hh:mm and a duration
// in hours. Helps keep tests readable.
func seg(base time.Time, startHour, startMin int, hours float64) sleepSegment {
	s := time.Date(base.Year(), base.Month(), base.Day(), startHour, startMin, 0, 0, base.Location())
	return sleepSegment{Start: s, End: s.Add(time.Duration(hours * float64(time.Hour)))}
}

// fragmentMain breaks a single main-sleep interval into k equal-length
// stage fragments, simulating how HAE delivers a night as many deep/rem/
// core rows (each its own metric_points entry). The pure logic must
// merge them back into one main sleep.
func fragmentMain(start time.Time, hours float64, k int) []sleepSegment {
	if k < 1 {
		k = 1
	}
	out := make([]sleepSegment, 0, k)
	chunk := hours / float64(k)
	cur := start
	for i := 0; i < k; i++ {
		end := cur.Add(time.Duration(chunk * float64(time.Hour)))
		out = append(out, sleepSegment{Start: cur, End: end})
		cur = end
	}
	return out
}

func TestPickMainSleep_NormalNight(t *testing.T) {
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc) // midnight ending May 13
	// Asleep 23:30 May 12 → 07:15 May 13.
	main := fragmentMain(d.Add(-30*time.Minute), 7.75, 5)
	merged := mergeSegments(main, mergeTolerance)
	if len(merged) != 1 {
		t.Fatalf("expected merge to 1 run, got %d", len(merged))
	}
	got, ok := pickMainSleep(merged, d)
	if !ok {
		t.Fatal("expected to find a main sleep")
	}
	wantWake := time.Date(2026, 5, 13, 7, 15, 0, 0, loc)
	if !got.End.Equal(wantWake) {
		t.Fatalf("wake = %v, want %v", got.End, wantWake)
	}
}

func TestPickMainSleep_LateNight(t *testing.T) {
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	// Asleep 02:00 → 10:00 May 13. Midpoint = 06:00, within ±6h. Wake = 10:00.
	main := fragmentMain(time.Date(2026, 5, 13, 2, 0, 0, 0, loc), 8.0, 4)
	got, ok := pickMainSleep(mergeSegments(main, mergeTolerance), d)
	if !ok {
		t.Fatal("late-night main sleep should still qualify (midpoint exactly +6h boundary)")
	}
	wantWake := time.Date(2026, 5, 13, 10, 0, 0, 0, loc)
	if !got.End.Equal(wantWake) {
		t.Fatalf("wake = %v, want %v", got.End, wantWake)
	}
}

func TestPickMainSleep_BiphasicSiesta(t *testing.T) {
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	// Main 23:00 May 12 → 07:00 May 13 (8h). Siesta 14:00 → 16:00 (2h).
	// Main midpoint = 03:00 May 13, within ±6h. Siesta midpoint = 15:00,
	// 15h from midnight d / 9h from midnight d+1 — out of range either
	// way. Algorithm must pick main.
	all := append(fragmentMain(d.Add(-1*time.Hour), 8.0, 4),
		seg(d, 14, 0, 2.0))
	got, ok := pickMainSleep(mergeSegments(all, mergeTolerance), d)
	if !ok {
		t.Fatal("biphasic main sleep should qualify")
	}
	wantWake := time.Date(2026, 5, 13, 7, 0, 0, 0, loc)
	if !got.End.Equal(wantWake) {
		t.Fatalf("wake = %v, want %v (siesta polluted the pick?)", got.End, wantWake)
	}
}

func TestPickMainSleep_JetLag(t *testing.T) {
	// User crashed at 18:00, woke at 02:00 next day. The function call
	// asks for night-STARTING-on-d (i.e. anchored to midnight d+1).
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	midnightDPlus1 := time.Date(2026, 5, 14, 0, 0, 0, 0, loc)
	// Asleep 18:00 d → 02:00 d+1 (8h). Midpoint = 22:00 d, which is
	// 2h before midnight d+1 — within ±6h.
	main := fragmentMain(time.Date(2026, 5, 13, 18, 0, 0, 0, loc), 8.0, 4)
	got, ok := pickMainSleep(mergeSegments(main, mergeTolerance), midnightDPlus1)
	if !ok {
		t.Fatal("jet-lag sleep should qualify for night starting on d")
	}
	if got.Start != time.Date(2026, 5, 13, 18, 0, 0, 0, loc) {
		t.Fatalf("onset = %v, want 2026-05-13 18:00 UTC", got.Start)
	}
	// And for night-ending-on-d there should be NOTHING qualifying — no
	// sleep within ±6h of d 00:00.
	if _, ok := pickMainSleep(mergeSegments(main, mergeTolerance), d); ok {
		t.Fatal("night-ending-on-d should be empty under jet-lag scenario")
	}
}

func TestPickMainSleep_EqualSplitTieBreaker(t *testing.T) {
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	// Two 4h sleeps, both midpoints within ±6h of midnight d.
	a := seg(d.Add(-3*time.Hour), 21, 0, 4.0) // 21:00 May 12 → 01:00 May 13, midpoint 23:00 May 12 (1h before d midnight)
	b := seg(d, 2, 0, 4.0)                    // 02:00 May 13 → 06:00 May 13, midpoint 04:00 (4h after d midnight)
	got, ok := pickMainSleep(mergeSegments([]sleepSegment{a, b}, mergeTolerance), d)
	if !ok {
		t.Fatal("expected a pick")
	}
	// Equal duration → later-ending wins. b ends 06:00, a ends 01:00.
	wantWake := time.Date(2026, 5, 13, 6, 0, 0, 0, loc)
	if !got.End.Equal(wantWake) {
		t.Fatalf("wake = %v, want %v (tie-break by later end failed)", got.End, wantWake)
	}
}

func TestPickMainSleep_NoQualifying(t *testing.T) {
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	if _, ok := pickMainSleep(nil, d); ok {
		t.Fatal("nil input should not qualify")
	}
	// Sleep mid-day far from any midnight.
	far := seg(d, 11, 0, 2.0) // 11:00 → 13:00, midpoint 12:00, 12h from midnight either way
	if _, ok := pickMainSleep(mergeSegments([]sleepSegment{far}, mergeTolerance), d); ok {
		t.Fatal("mid-day nap should not qualify as main sleep for night ending on d")
	}
}

func TestPickMainSleep_ShiftWorkFallsThrough(t *testing.T) {
	// Documents the known limitation: a true shift-worker sleep
	// 06:00→14:00 has midpoint 10:00, which is 10h from any midnight.
	// The spec's ±6h rule excludes it; consumer falls back to imputed.
	loc := time.UTC
	d := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	main := fragmentMain(time.Date(2026, 5, 13, 6, 0, 0, 0, loc), 8.0, 4)
	if _, ok := pickMainSleep(mergeSegments(main, mergeTolerance), d); ok {
		t.Fatal("shift-work sleep should NOT qualify under strict ±6h rule (documented limitation)")
	}
	if _, ok := pickMainSleep(mergeSegments(main, mergeTolerance), d.AddDate(0, 0, 1)); ok {
		t.Fatal("shift-work sleep should not qualify for d+1 midnight either")
	}
}

func TestMergeSegments_BridgesShortAwake(t *testing.T) {
	loc := time.UTC
	base := time.Date(2026, 5, 13, 1, 0, 0, 0, loc)
	// Two segments with 20-min gap (mid-night arousal) — should merge.
	a := sleepSegment{Start: base, End: base.Add(3 * time.Hour)}
	b := sleepSegment{Start: base.Add(3*time.Hour + 20*time.Minute), End: base.Add(7 * time.Hour)}
	merged := mergeSegments([]sleepSegment{a, b}, mergeTolerance)
	if len(merged) != 1 {
		t.Fatalf("expected 1 merged run, got %d", len(merged))
	}
	if merged[0].Start != a.Start || merged[0].End != b.End {
		t.Fatalf("merge endpoints wrong: got %v..%v want %v..%v",
			merged[0].Start, merged[0].End, a.Start, b.End)
	}
}

func TestMergeSegments_DoesNotBridgeLongGap(t *testing.T) {
	loc := time.UTC
	base := time.Date(2026, 5, 13, 1, 0, 0, 0, loc)
	// 90-min gap exceeds mergeTolerance — must stay split.
	a := sleepSegment{Start: base, End: base.Add(2 * time.Hour)}
	b := sleepSegment{Start: base.Add(3*time.Hour + 30*time.Minute), End: base.Add(6 * time.Hour)}
	merged := mergeSegments([]sleepSegment{a, b}, mergeTolerance)
	if len(merged) != 2 {
		t.Fatalf("expected 2 separate runs, got %d", len(merged))
	}
}

func TestMergeSegments_OverlappingAndUnsorted(t *testing.T) {
	loc := time.UTC
	base := time.Date(2026, 5, 13, 0, 0, 0, 0, loc)
	a := sleepSegment{Start: base.Add(1 * time.Hour), End: base.Add(3 * time.Hour)}
	b := sleepSegment{Start: base.Add(2 * time.Hour), End: base.Add(4 * time.Hour)} // overlaps a
	c := sleepSegment{Start: base, End: base.Add(2 * time.Hour)}                    // overlaps a from before
	merged := mergeSegments([]sleepSegment{a, b, c}, mergeTolerance)
	if len(merged) != 1 {
		t.Fatalf("overlapping segs should collapse to 1, got %d", len(merged))
	}
	if merged[0].Start != c.Start || merged[0].End != b.End {
		t.Fatalf("merge envelope wrong: got %v..%v want %v..%v", merged[0].Start, merged[0].End, c.Start, b.End)
	}
}

func TestPickWinningSource_AppleOverRing(t *testing.T) {
	totals := map[string]float64{
		"Alexey's Apple Watch Ultra": 7.5,
		"RingConn":                   8.2,
	}
	if got := pickWinningSource(totals); got != "Alexey's Apple Watch Ultra" {
		t.Fatalf("Apple Watch must win over RingConn, got %q", got)
	}
}

func TestPickWinningSource_AppleSubHourFalls(t *testing.T) {
	// Apple Watch with <1h is wisp — RingConn with real night wins.
	totals := map[string]float64{
		"Alexey's Apple Watch Ultra": 0.4,
		"RingConn":                   7.9,
	}
	if got := pickWinningSource(totals); got != "RingConn" {
		t.Fatalf("RingConn should win over watch wisp, got %q", got)
	}
}

func TestPickWinningSource_FallbackHighest(t *testing.T) {
	totals := map[string]float64{
		"SomeRandomApp": 6.0,
		"Other":         2.0,
	}
	if got := pickWinningSource(totals); got != "SomeRandomApp" {
		t.Fatalf("highest-hours fallback should win, got %q", got)
	}
}

func TestPickWinningSource_Empty(t *testing.T) {
	if got := pickWinningSource(nil); got != "" {
		t.Fatalf("empty input should return empty, got %q", got)
	}
}

func TestHourOfDay(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		t    time.Time
		want float64
	}{
		{time.Date(2026, 5, 13, 7, 30, 0, 0, loc), 7.5},
		{time.Date(2026, 5, 13, 0, 0, 0, 0, loc), 0.0},
		{time.Date(2026, 5, 13, 23, 59, 0, 0, loc), 23.0 + 59.0/60.0},
	}
	for _, c := range cases {
		got := hourOfDay(c.t)
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("hourOfDay(%v) = %v, want %v", c.t, got, c.want)
		}
	}
}
