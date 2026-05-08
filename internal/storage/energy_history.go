package storage

import (
	"fmt"
	"log"

	"health-receiver/internal/health"
)

// EnergyHistoryPoint is one day of the EnergyBank trend chart. Capacity is
// what the user started the day with; CurrentEOD is what was left at the
// last snapshot (typically end-of-day after all sync ticks); Drain is the
// cumulative cost; Verdict is the prescriptive action that fell out of
// the formula. All four come straight from health.EnergyBank.
type EnergyHistoryPoint struct {
	Date       string `json:"date"`
	Capacity   int    `json:"capacity"`
	CurrentEOD int    `json:"current_eod"`
	Drain      int    `json:"drain"`
	Verdict    string `json:"verdict"`
}

// SaveEnergyBankSnapshot upserts the EOD snapshot of the day's EnergyBank
// into daily_scores. Best-effort — errors are logged but not returned, and
// callers should never block briefing rendering on this.
//
// Called from the briefing path on every render so the latest in-memory
// EnergyBank for `date` is the value that lands. By construction the
// previous day's row freezes once today rolls over (no more recompute for
// it from briefing). Backfilling historical rows is intentionally out of
// scope — adding that prematurely would lock in numbers from an evolving
// formula. Track via Todoist: 6gX922PFjx82PvGf.
func (s *DB) SaveEnergyBankSnapshot(date string, eb *health.EnergyBank) {
	if eb == nil || date == "" {
		return
	}
	ctx, cancel := queryCtx()
	defer cancel()
	_, err := s.pool.Exec(ctx, `
		INSERT INTO daily_scores (date, energy_capacity, energy_eod_current, energy_drain, energy_verdict, computed_at)
		VALUES ($1, $2, $3, $4, $5, NOW()::text)
		ON CONFLICT(date) DO UPDATE SET
			energy_capacity    = excluded.energy_capacity,
			energy_eod_current = excluded.energy_eod_current,
			energy_drain       = excluded.energy_drain,
			energy_verdict     = excluded.energy_verdict,
			computed_at        = excluded.computed_at`,
		date, eb.Capacity, eb.Current, eb.DrainSoFar, eb.ActionVerdict)
	if err != nil {
		log.Printf("save energy bank snapshot %s: %v", date, err)
	}
}

// GetEnergyHistory returns the most recent `days` EOD snapshots in
// ascending date order. Rows without an energy_verdict (snapshot never
// taken) are skipped — the sparkline shouldn't render a flat line for
// pre-persistence days.
func (s *DB) GetEnergyHistory(days int) ([]EnergyHistoryPoint, error) {
	if days <= 0 {
		days = 14
	}
	if days > 365 {
		days = 365
	}
	ctx, cancel := queryCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT date, energy_capacity, energy_eod_current, energy_drain, energy_verdict
		FROM daily_scores
		WHERE energy_verdict IS NOT NULL
		ORDER BY date DESC
		LIMIT $1`, days)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []EnergyHistoryPoint
	for rows.Next() {
		var p EnergyHistoryPoint
		var capPtr, curPtr, drainPtr *int
		var verdict *string
		if err := rows.Scan(&p.Date, &capPtr, &curPtr, &drainPtr, &verdict); err != nil {
			return nil, fmt.Errorf("scan energy history row: %w", err)
		}
		if capPtr != nil {
			p.Capacity = *capPtr
		}
		if curPtr != nil {
			p.CurrentEOD = *curPtr
		}
		if drainPtr != nil {
			p.Drain = *drainPtr
		}
		if verdict != nil {
			p.Verdict = *verdict
		}
		out = append(out, p)
	}
	// Reverse to ascending order.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, rows.Err()
}
