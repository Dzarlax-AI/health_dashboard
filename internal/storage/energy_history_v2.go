package storage

import (
	"context"
	"encoding/json"
	"time"
)

// EnergySnapshotPoint is one row of the EnergyBank v2 hourly history,
// suitable for direct JSON encoding to the /api/energy-history?granularity=hour
// response. The fields mirror energy_snapshots one-to-one with two
// adaptations: TS is rendered in the tenant's TZ (so UI clients don't
// have to do their own TZ math), and Components is exposed as
// json.RawMessage so a malformed legacy row can't fail the entire
// response — it just renders as null.
//
// FormulaVersion is loaded for every row (so the handler can pick the
// freshest one for the response envelope) but suppressed in JSON via
// json:"-" — it's redundant once the envelope carries it, and avoiding
// per-point repetition trims a non-trivial number of bytes off a
// max-size response (8640 buckets × ≈18 bytes per "formula_version":1).
type EnergySnapshotPoint struct {
	TS             time.Time       `json:"ts"`
	Bank           int             `json:"bank"`
	DrainDelta     int             `json:"drain_delta"`
	RestoreDelta   int             `json:"restore_delta"`
	FormulaVersion int             `json:"-"`
	Flags          []string        `json:"flags"`
	Components     json.RawMessage `json:"components,omitempty"`
}

// GetEnergyHistoryV2 returns energy_snapshots rows for the last
// `hours` hours in ascending ts_bucket order. `tz` is used to render
// each ts in the tenant's local zone; the underlying TIMESTAMPTZ
// values stay correct regardless, but the JSON output uses local
// offsets so consumers don't have to translate. `hours` is clamped to
// [1, 720] (30 days) — past that, hourly granularity is wasted bytes
// and the day endpoint should be used instead.
//
// State is NOT a column on energy_snapshots — it's a derived property
// of the iteration that produced each snapshot. We don't backfill it
// per-row; the API's top-level "state" comes from the freshest row
// (caller responsibility).
func (s *DB) GetEnergyHistoryV2(ctx context.Context, tz string, hours int) ([]EnergySnapshotPoint, error) {
	if hours <= 0 {
		hours = 72
	}
	if hours > 720 {
		hours = 720
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, err
	}

	// `since` is in UTC; ts_bucket is TIMESTAMPTZ. PG converts both
	// to UTC internally for the comparison, so subtracting `hours`
	// from server-now matches subtracting `hours` from tenant-local-now
	// — they're the same instant in absolute time. The TZ only
	// matters for *rendering* the output (p.TS = ts.In(loc) below),
	// not for the range filter.
	since := time.Now().Add(-time.Duration(hours) * time.Hour)
	rows, err := s.pool.Query(ctx, `
		SELECT ts_bucket, bank, drain_delta, restore_delta, formula_version, flags, components
		FROM energy_snapshots
		WHERE ts_bucket >= $1
		ORDER BY ts_bucket ASC`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []EnergySnapshotPoint{}
	for rows.Next() {
		var p EnergySnapshotPoint
		var ts time.Time
		var components []byte
		if err := rows.Scan(&ts, &p.Bank, &p.DrainDelta, &p.RestoreDelta,
			&p.FormulaVersion, &p.Flags, &components); err != nil {
			return nil, err
		}
		p.TS = ts.In(loc)
		if p.Flags == nil {
			p.Flags = []string{}
		}
		if len(components) > 0 {
			p.Components = json.RawMessage(components)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
