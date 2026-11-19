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

type Candidate struct {
	ID            string        `json:"id"`
	Revision      int64         `json:"revision"`
	Priority      int           `json:"priority"`
	Zone          string        `json:"wake_zone"`
	Stages        []Stage       `json:"stages"`
	ZeroStages    []string      `json:"zero_stages,omitempty"`
	LegacyAliases []LegacyAlias `json:"legacy_aliases"`
}

func (c *Candidate) WakeDate() (string, error) {
	if strings.TrimSpace(c.ID) == "" || c.Revision < 1 || c.Priority < 0 {
		return "", errors.New("candidate requires id, positive revision and nonnegative priority")
	}
	var lastAsleep time.Time
	seen := make(map[string]bool, len(c.Stages))
	observed := make(map[string]bool, 5)
	for i := range c.Stages {
		point := &c.Stages[i]
		if point.ID == "" || seen[point.ID] || point.Start.IsZero() || !point.End.After(point.Start) {
			return "", errors.New("stages require distinct ids and increasing absolute bounds")
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
	if c.Zone == "" || c.Zone == "Local" {
		return "", ErrUnknownWakeZone
	}
	zone, err := time.LoadLocation(c.Zone)
	if err != nil {
		return "", ErrUnknownWakeZone
	}
	return lastAsleep.In(zone).Format(time.DateOnly), nil
}
