package sleepstudy

import (
	"slices"
	"time"
)

const (
	HistoryDays      = 28
	HistoryMinDays   = 7
	maxWakeDeviation = 3 * time.Hour
)

// PriorDay groups normalized episodes with the same historical wake date.
// AvailableAt is the latest observation or revision timestamp for this day.
type PriorDay struct {
	Date        string
	Zone        string
	AvailableAt time.Time
	Episodes    []Episode
}

type anchor struct {
	wake     time.Duration
	duration time.Duration
}

// HistoricalWindow estimates a local sleep window using only previous days.
// A nil cutoff means latest-revision replay. A cutoff requires evidence that
// each historical observation was already available. The bounds are research
// parameters used to abstain on irregular inputs.
func HistoricalWindow(date, zone string, cutoff *time.Time, history []PriorDay) *Window {
	location, err := time.LoadLocation(zone)
	if err != nil || zone == "" || zone == "Local" {
		return nil
	}
	target, err := time.Parse(time.DateOnly, date)
	if err != nil {
		return nil
	}
	from := target.AddDate(0, 0, -HistoryDays).Format(time.DateOnly)
	observations := make([]anchor, 0, HistoryDays)
	seen := make(map[string]bool)
	for _, day := range history {
		if day.Date < from || day.Date >= date || day.Zone != zone {
			continue
		}
		if cutoff != nil && (day.AvailableAt.IsZero() || day.AvailableAt.After(*cutoff)) {
			continue
		}
		if seen[day.Date] {
			return nil
		}
		seen[day.Date] = true
		if a, ok := historicalAnchor(day.Episodes, location); ok {
			observations = append(observations, a)
		}
	}
	if len(observations) < HistoryMinDays {
		return nil
	}
	bestWake := observations[0].wake
	bestCost := time.Duration(1<<63 - 1)
	for _, a := range observations {
		var cost time.Duration
		for _, b := range observations {
			cost += circularDistance(a.wake, b.wake)
		}
		if cost < bestCost || (cost == bestCost && a.wake < bestWake) {
			bestWake, bestCost = a.wake, cost
		}
	}
	durations := make([]time.Duration, 0, len(observations))
	for _, a := range observations {
		if circularDistance(a.wake, bestWake) <= maxWakeDeviation {
			durations = append(durations, a.duration)
		}
	}
	if len(durations) < HistoryMinDays || len(durations)*3 < len(observations)*2 {
		return nil
	}
	slices.Sort(durations)
	typical := durations[len(durations)/2]
	wake, unique := uniqueWallTime(target.Add(bestWake), location)
	if !unique {
		return nil
	}
	return &Window{
		Start:     wake.Add(-typical - maxWakeDeviation),
		End:       wake.Add(maxWakeDeviation),
		MinAsleep: typical / 2,
	}
}

// uniqueWallTime interprets the UTC clock fields as local wall fields.
// A clock gap has no realizations; a repeated clock hour has two. Neither is
// suitable for selecting an episode from a learned clock time alone.
func uniqueWallTime(wall time.Time, location *time.Location) (time.Time, bool) {
	const radius = 36 * time.Hour
	var match time.Time
	for probe, end := wall.Add(-radius), wall.Add(radius); !probe.After(end); {
		local := probe.In(location)
		_, offset := local.Zone()
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		observed := candidate.In(location)
		_, observedOffset := observed.Zone()
		if observedOffset == offset {
			if !match.IsZero() && !match.Equal(candidate) {
				return time.Time{}, false
			}
			match = candidate
		}
		_, next := local.ZoneBounds()
		if next.IsZero() {
			break
		}
		probe = next
	}
	return match.In(location), !match.IsZero()
}

func historicalAnchor(episodes []Episode, location *time.Location) (anchor, bool) {
	var largest []Span
	var duration time.Duration
	for _, e := range episodes {
		if !e.Covered {
			continue
		}
		spans := Union(e.Asleep)
		if d := Duration(spans); d > duration {
			largest, duration = spans, d
		}
	}
	if duration == 0 {
		return anchor{}, false
	}
	envelope := Span{Start: largest[0].Start, End: largest[len(largest)-1].End}
	for _, e := range episodes {
		if !e.Covered {
			continue
		}
		d := Duration(e.Asleep)
		if d > 0 && duration-d < 30*time.Minute && Overlap(e.Asleep, envelope) < d*9/10 {
			return anchor{}, false
		}
	}
	end := envelope.End.In(location)
	return anchor{wake: time.Duration(end.Hour())*time.Hour + time.Duration(end.Minute())*time.Minute, duration: duration}, true
}

func circularDistance(a, b time.Duration) time.Duration {
	d := max(a-b, b-a)
	return min(d, 24*time.Hour-d)
}
