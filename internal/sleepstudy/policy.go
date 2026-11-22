// Package sleepstudy contains the deliberately conservative shadow policy for
// choosing a main sleep episode. It has no storage, clock, or production
// dependencies.
package sleepstudy

import (
	"sort"
	"time"
)

type Span struct{ Start, End time.Time }

type Episode struct {
	ID       string
	Source   string
	Priority int
	Asleep   []Span
	Covered  bool
}

type Window struct {
	Start, End time.Time
	MinAsleep  time.Duration
}

type Decision struct {
	Main       string
	Additional []string
	Uncertain  []string
	Reason     string
	Assisted   bool
}

const decisiveOverlap = 30 * time.Minute

// Union returns sorted, non-overlapping positive spans. Touching spans are
// joined because they describe one continuous interval.
func Union(spans []Span) []Span {
	valid := make([]Span, 0, len(spans))
	for _, s := range spans {
		if s.Start.Before(s.End) {
			valid = append(valid, s)
		}
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].Start.Before(valid[j].Start) })
	out := make([]Span, 0, len(valid))
	for _, s := range valid {
		if len(out) == 0 || s.Start.After(out[len(out)-1].End) {
			out = append(out, s)
			continue
		}
		if s.End.After(out[len(out)-1].End) {
			out[len(out)-1].End = s.End
		}
	}
	return out
}

func Duration(spans []Span) time.Duration {
	var d time.Duration
	for _, s := range Union(spans) {
		d += s.End.Sub(s.Start)
	}
	return d
}

func Overlap(spans []Span, bound Span) time.Duration {
	if !bound.Start.Before(bound.End) {
		return 0
	}
	var d time.Duration
	for _, s := range Union(spans) {
		start, end := s.Start, s.End
		if start.Before(bound.Start) {
			start = bound.Start
		}
		if end.After(bound.End) {
			end = bound.End
		}
		if start.Before(end) {
			d += end.Sub(start)
		}
	}
	return d
}

// Group groups positive spans whose intervening gap is at most maxGap.
func Group(spans []Span, maxGap time.Duration) [][]Span {
	u := Union(spans)
	if len(u) == 0 {
		return nil
	}
	out := make([][]Span, 0, len(u))
	for _, s := range u {
		if len(out) == 0 || s.Start.Sub(out[len(out)-1][len(out[len(out)-1])-1].End) > maxGap {
			out = append(out, []Span{s})
		} else {
			out[len(out)-1] = append(out[len(out)-1], s)
		}
	}
	return out
}

type candidate struct {
	e       Episode
	asleep  []Span
	overlap time.Duration
}

func invalidEpisode(e Episode) bool {
	if e.ID == "" {
		return true
	}
	for _, s := range e.Asleep {
		if !s.Start.Before(s.End) {
			return true
		}
	}
	return len(Union(e.Asleep)) == 0
}

func sameSpans(a, b []Span) bool {
	a, b = Union(a), Union(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Start.Equal(b[i].Start) || !a[i].End.Equal(b[i].End) {
			return false
		}
	}
	return true
}

func equivalent(a, b candidate) bool {
	if sameSpans(a.asleep, b.asleep) {
		return true
	}
	// Conservative envelope equivalence: both candidates must cover almost all
	// of one another, preventing a short Watch night from replacing a full one.
	ua, ub := Union(a.asleep), Union(b.asleep)
	if len(ua) == 0 || len(ub) == 0 {
		return false
	}
	envelope := func(x []Span) Span { return Span{x[0].Start, x[len(x)-1].End} }
	if !envelope(ua).Start.Equal(envelope(ub).Start) || !envelope(ua).End.Equal(envelope(ub).End) {
		return false
	}
	common := intersectionDuration(ua, ub)
	return common*100 >= Duration(ua)*90 && common*100 >= Duration(ub)*90
}

func intersectionDuration(a, b []Span) time.Duration {
	var d time.Duration
	for _, s := range b {
		d += Overlap(a, s)
	}
	return d
}

func Select(episodes []Episode, window *Window, anchorID string) Decision {
	return selectEpisode(episodes, window, anchorID, "")
}

// SelectPreferred applies a tenant's exact source identity to candidates for
// the same main sleep after coverage, minimum duration and ambiguity checks.
func SelectPreferred(episodes []Episode, window *Window, preferred string) Decision {
	return selectEpisode(episodes, window, "", preferred)
}

func selectEpisode(episodes []Episode, window *Window, anchorID, preferred string) Decision {
	d := Decision{}
	seen := map[string]bool{}
	for _, e := range episodes {
		if seen[e.ID] || invalidEpisode(e) {
			d.Reason = "invalid_data"
			return d
		}
		seen[e.ID] = true
	}
	if window != nil && !window.Start.Before(window.End) {
		d.Reason = "invalid_window"
		return d
	}
	d.Assisted = anchorID != ""
	cs := make([]candidate, 0, len(episodes))
	covered := make([]candidate, 0, len(episodes))
	for _, e := range episodes {
		if !e.Covered {
			continue
		}
		c := candidate{e: e, asleep: Union(e.Asleep)}
		covered = append(covered, c)
		if window != nil {
			c.overlap = Overlap(c.asleep, Span{window.Start, window.End})
			if c.overlap < window.MinAsleep {
				continue
			}
		}
		cs = append(cs, c)
	}
	if anchorID != "" {
		found := -1
		for i := range cs {
			if cs[i].e.ID == anchorID {
				if found >= 0 {
					d.Reason = "invalid_anchor"
					return d
				}
				found = i
			}
		}
		if found < 0 {
			d.Reason = "invalid_anchor"
			return d
		}
		selected := cs[found]
		return finish(selected, covered, window, d)
	}
	if window == nil {
		d.Reason = "blind_mode"
		return d
	}
	if len(cs) == 0 {
		d.Reason = "below_threshold"
		return d
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].overlap != cs[j].overlap {
			return cs[i].overlap > cs[j].overlap
		}
		if cs[i].e.Priority != cs[j].e.Priority {
			return cs[i].e.Priority < cs[j].e.Priority
		}
		return cs[i].e.ID < cs[j].e.ID
	})
	best := cs[0]
	for i := 1; i < len(cs); i++ {
		other := cs[i]
		if best.overlap-other.overlap < decisiveOverlap && !equivalent(best, other) {
			d.Reason = "ambiguous_overlap"
			d.Uncertain = []string{best.e.ID, other.e.ID}
			return d
		}
		if equivalent(best, other) && best.overlap == other.overlap {
			if other.e.Priority < best.e.Priority || (other.e.Priority == best.e.Priority && other.e.ID < best.e.ID) {
				best = other
			}
		}
	}
	if preferred != "" && best.e.Source != preferred {
		for _, c := range cs {
			// A separate nap can also intersect a broad personal sleep window.
			// Require observations of the same episode before applying preference.
			if c.e.Source == preferred && intersectionDuration(c.asleep, best.asleep) > 0 {
				best = c
				break
			}
		}
	}
	return finish(best, covered, window, d)
}

func finish(selected candidate, all []candidate, window *Window, d Decision) Decision {
	d.Main = selected.e.ID
	for _, c := range all {
		if c.e.ID == selected.e.ID || equivalent(c, selected) {
			continue
		}
		disjoint := Overlap(c.asleep, Span{selected.asleep[0].Start, selected.asleep[len(selected.asleep)-1].End}) == 0
		afterWake := c.asleep[0].Start.Sub(selected.asleep[len(selected.asleep)-1].End) >= decisiveOverlap
		separate := afterWake
		if window != nil {
			overlap := Overlap(c.asleep, Span{window.Start, window.End})
			// A shorter separate episode after a confirmed wake remains
			// additional even when the personal window ends conservatively late.
			separate = Duration(c.asleep) < window.MinAsleep && (overlap == 0 || afterWake)
		}
		if disjoint && separate {
			d.Additional = append(d.Additional, c.e.ID)
		} else {
			d.Uncertain = append(d.Uncertain, c.e.ID)
		}
	}
	d.Reason = "selected_covered"
	return d
}
