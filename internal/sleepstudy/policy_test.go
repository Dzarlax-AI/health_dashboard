package sleepstudy

import (
	"testing"
	"time"
)

var day = time.Date(2026, 8, 9, 0, 0, 0, 0, time.UTC)

func span(h1, m1, h2, m2 int) Span {
	return Span{day.Add(time.Duration(h1)*time.Hour + time.Duration(m1)*time.Minute), day.Add(time.Duration(h2)*time.Hour + time.Duration(m2)*time.Minute)}
}
func overnight(h1, m1, h2, m2 int) Span {
	s := span(h1, m1, h2, m2)
	if !s.Start.Before(s.End) {
		s.End = s.End.Add(24 * time.Hour)
	}
	return s
}
func window(h1, m1, h2, m2 int, min time.Duration) *Window {
	s := overnight(h1, m1, h2, m2)
	return &Window{Start: s.Start, End: s.End, MinAsleep: min}
}
func ep(id, source string, priority int, asleep []Span) Episode {
	return Episode{ID: id, Source: source, Priority: priority, Asleep: asleep, Covered: true}
}

func TestSelectEquivalentNineAugustAndTimeSeparatedAdditional(t *testing.T) {
	a := ep("ringconn", "RingConn", 20, []Span{overnight(22, 0, 7, 3)})
	b := ep("watch", "Watch", 10, []Span{overnight(22, 0, 7, 3)})
	nap := ep("nap", "Watch", 10, []Span{span(13, 0, 14, 8)})
	d := Select([]Episode{a, b, nap}, window(21, 0, 8, 30, 8*time.Hour), "")
	if d.Main != "watch" || len(d.Additional) != 1 || d.Additional[0] != "nap" {
		t.Fatalf("decision=%+v", d)
	}
}

func TestSelectNoShortWatchOverSuitableRingConn(t *testing.T) {
	ring := ep("ring", "RingConn", 20, []Span{overnight(22, 0, 7, 0)})
	watch := ep("watch", "Watch", 1, []Span{overnight(23, 0, 5, 0)})
	d := Select([]Episode{ring, watch}, window(21, 0, 8, 0, 8*time.Hour), "")
	if d.Main != "ring" {
		t.Fatalf("decision=%+v", d)
	}
}

func TestSelectAnchorAndAmbiguity(t *testing.T) {
	a := ep("a", "x", 1, []Span{overnight(22, 0, 6, 0)})
	b := ep("b", "y", 1, []Span{overnight(22, 15, 6, 0)})
	if d := Select([]Episode{a, b}, window(21, 0, 8, 0, 1*time.Hour), "a"); d.Main != "a" || !d.Assisted {
		t.Fatalf("anchor=%+v", d)
	}
	if d := Select([]Episode{a, b}, window(21, 0, 8, 0, 1*time.Hour), "missing"); d.Reason != "invalid_anchor" {
		t.Fatalf("invalid anchor=%+v", d)
	}
	if d := Select([]Episode{a, b}, window(21, 0, 8, 0, 1*time.Hour), ""); d.Reason != "ambiguous_overlap" {
		t.Fatalf("ambiguity=%+v", d)
	}
}

func TestSelectShiftWorkerDaytimeAndNoHistory(t *testing.T) {
	daytime := ep("work", "worker", 1, []Span{span(9, 0, 17, 0)})
	d := Select([]Episode{daytime}, window(8, 0, 18, 0, 7*time.Hour), "")
	if d.Main != "work" {
		t.Fatalf("daytime=%+v", d)
	}
	if d := Select([]Episode{daytime}, nil, ""); d.Reason != "blind_mode" {
		t.Fatalf("no history=%+v", d)
	}
}

func TestSelectSourceOverlapIsUncertain(t *testing.T) {
	a := ep("a", "x", 1, []Span{overnight(22, 0, 6, 0)})
	b := ep("b", "y", 1, []Span{overnight(22, 20, 6, 0)})
	d := Select([]Episode{a, b}, window(21, 0, 8, 0, 1*time.Hour), "")
	if d.Reason != "ambiguous_overlap" || len(d.Uncertain) != 2 {
		t.Fatalf("decision=%+v", d)
	}
}

func TestGroupGapBoundaries(t *testing.T) {
	for _, tc := range []struct {
		gap  time.Duration
		want int
	}{{29*time.Minute + 59*time.Second, 1}, {30 * time.Minute, 1}, {30*time.Minute + time.Second, 2}} {
		spans := []Span{{day, day.Add(time.Hour)}, {day.Add(time.Hour + tc.gap), day.Add(2*time.Hour + tc.gap)}}
		if got := len(Group(spans, 30*time.Minute)); got != tc.want {
			t.Errorf("gap %v: got %d want %d", tc.gap, got, tc.want)
		}
	}
}

func TestInvalidSpansAndUnion(t *testing.T) {
	if d := Select([]Episode{ep("bad", "x", 1, []Span{{day, day}})}, &Window{day, day.Add(time.Hour), 0}, ""); d.Reason != "invalid_data" {
		t.Fatalf("invalid=%+v", d)
	}
	spans := []Span{{day, day.Add(time.Hour)}, {day.Add(30 * time.Minute), day.Add(2 * time.Hour)}}
	if Duration(spans) != 2*time.Hour || Overlap(spans, Span{day.Add(90 * time.Minute), day.Add(3 * time.Hour)}) != 30*time.Minute {
		t.Fatalf("union/duration/overlap failed")
	}
}
