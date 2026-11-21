// Package sleepwire validates the versioned main-night sync contract.
package sleepwire

import (
	"errors"
	"slices"
	"strings"
	"time"
)

var ErrUnknownWakeZone = errors.New("unknown wake zone")

type LegacyAlias struct {
	SourceEpoch string `json:"source_epoch"`
	Source      string `json:"source"`
	Generation  string `json:"sync_generation"`
}

func (a LegacyAlias) Valid() bool {
	return a.SourceEpoch != "" && a.Source != "" && a.Generation != ""
}

type Stage struct {
	ID    string    `json:"id"`
	Kind  string    `json:"kind"`
	Start time.Time `json:"start_at"`
	End   time.Time `json:"end_at"`
}

// Coverage attests that an authoritative source read completed through End.
// The producer supplies the same stable source identity as the packet envelope.
type Coverage struct {
	SourceID   string    `json:"source_id"`
	Revision   int64     `json:"revision"`
	Start      time.Time `json:"start_at"`
	End        time.Time `json:"end_at"`
	ObservedAt time.Time `json:"observed_at"`
}

func (c Coverage) Contains(start, end time.Time) bool {
	return !start.IsZero() && end.After(start) && !start.Before(c.Start) && !end.After(c.End)
}

type Withdrawal struct {
	WakeDate         string `json:"wake_date"`
	PreviousRevision int64  `json:"previous_revision"`
}

type Candidate struct {
	ID            string        `json:"id"`
	Revision      int64         `json:"revision"`
	Priority      int           `json:"priority"`
	Zone          string        `json:"wake_zone"`
	Stages        []Stage       `json:"stages"`
	ZeroStages    []string      `json:"zero_stages,omitempty"`
	LegacyAliases []LegacyAlias `json:"legacy_aliases"`
	Coverage      *Coverage     `json:"coverage,omitempty"`
	Withdrawal    *Withdrawal   `json:"withdrawal,omitempty"`
}

func (c *Candidate) ValidateSource(sourceID string) (string, error) {
	if c.Coverage != nil && c.Coverage.SourceID != sourceID {
		return "", errors.New("coverage must belong to the candidate source")
	}
	return c.WakeDate()
}

func (c *Candidate) WakeDate() (string, error) {
	if strings.TrimSpace(c.ID) == "" || c.Revision < 1 || c.Priority < 0 {
		return "", errors.New("candidate requires id, positive revision and nonnegative priority")
	}
	if c.Zone == "" || c.Zone == "Local" {
		return "", ErrUnknownWakeZone
	}
	zone, err := time.LoadLocation(c.Zone)
	if err != nil {
		return "", ErrUnknownWakeZone
	}
	if proof := c.Coverage; proof != nil {
		if strings.TrimSpace(proof.SourceID) == "" || proof.Revision != c.Revision ||
			proof.Start.IsZero() || !proof.End.After(proof.Start) || proof.End.Sub(proof.Start) > 72*time.Hour ||
			proof.ObservedAt.Before(proof.End) {
			return "", errors.New("invalid source coverage")
		}
		proof.Start, proof.End, proof.ObservedAt = proof.Start.UTC(), proof.End.UTC(), proof.ObservedAt.UTC()
	}
	if w := c.Withdrawal; w != nil {
		if c.Coverage == nil || w.PreviousRevision < 1 || w.PreviousRevision >= c.Revision ||
			len(c.Stages) != 0 || len(c.ZeroStages) != 0 || len(c.LegacyAliases) != 0 {
			return "", errors.New("withdrawal requires coverage and a previous revision")
		}
		if _, err := time.Parse(time.DateOnly, w.WakeDate); err != nil {
			return "", errors.New("invalid withdrawal wake date")
		}
		c.Stages, c.ZeroStages, c.LegacyAliases = nil, nil, nil
		return w.WakeDate, nil
	}
	var lastAsleep time.Time
	seen := make(map[string]bool, len(c.Stages))
	observed := make(map[string]bool, 5)
	for i := range c.Stages {
		point := &c.Stages[i]
		if point.ID == "" || seen[point.ID] || point.Start.IsZero() || !point.End.After(point.Start) {
			return "", errors.New("stages require distinct ids and increasing absolute bounds")
		}
		if c.Coverage != nil && !c.Coverage.Contains(point.Start, point.End) {
			return "", errors.New("stage lies outside source coverage")
		}
		seen[point.ID] = true
		observed[point.Kind] = true
		point.Start, point.End = point.Start.UTC(), point.End.UTC()
		switch point.Kind {
		case "core", "deep", "rem", "asleep_unspecified":
			if point.End.After(lastAsleep) {
				lastAsleep = point.End
			}
		case "awake", "in_bed":
		default:
			return "", errors.New("unknown stage kind")
		}
	}
	if lastAsleep.IsZero() {
		return "", errors.New("candidate requires at least one asleep interval")
	}
	for _, kind := range c.ZeroStages {
		switch kind {
		case "core", "deep", "rem", "asleep_unspecified", "awake":
		default:
			return "", errors.New("unknown zero stage kind")
		}
		if observed[kind] {
			return "", errors.New("zero stage contradicts an observed interval")
		}
	}
	slices.Sort(c.ZeroStages)
	c.ZeroStages = slices.Compact(c.ZeroStages)
	for _, alias := range c.LegacyAliases {
		if !alias.Valid() {
			return "", errors.New("incomplete legacy alias")
		}
	}
	slices.SortFunc(c.Stages, func(a, b Stage) int { return strings.Compare(a.ID, b.ID) })
	slices.SortFunc(c.LegacyAliases, func(a, b LegacyAlias) int {
		if cmp := strings.Compare(a.SourceEpoch, b.SourceEpoch); cmp != 0 {
			return cmp
		}
		if cmp := strings.Compare(a.Source, b.Source); cmp != 0 {
			return cmp
		}
		return strings.Compare(a.Generation, b.Generation)
	})
	c.LegacyAliases = slices.Compact(c.LegacyAliases)
	if len(c.LegacyAliases) == 0 {
		c.LegacyAliases = nil
	}
	return lastAsleep.In(zone).Format(time.DateOnly), nil
}
