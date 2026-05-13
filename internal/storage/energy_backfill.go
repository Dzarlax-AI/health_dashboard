package storage

import (
	"context"
	"fmt"
	"log"
	"time"
)

// EnergyBackfillProgress is the running tally for one backfill pass.
// All fields are populated even on error so the caller / UI can render
// a partial result. Fields parallel the cmd/energy_backfill summary line.
type EnergyBackfillProgress struct {
	From    string `json:"from"`
	To      string `json:"to"`
	TZ      string `json:"tz"`
	Total   int    `json:"total"`   // total days in the window (To − From + 1)
	Done    int    `json:"done"`    // days processed so far
	OK      int    `json:"ok"`
	Skipped int    `json:"skipped"` // stale state — insufficient lookback
	Errors  int    `json:"errors"`
}

// BackfillEnergyRange replays the v2 formula over [from, to] and writes
// one EOD snapshot per date at 23:55 local in `tz`. Each row is flagged
// `backfilled` so calibration queries can filter live vs synthetic.
//
// The function is callable from cmd/energy_backfill (CLI), from the
// import-finished hook (auto-onboarding after Apple Health zip), and
// from the per-user settings endpoint (manual button). The progress
// callback (nil-safe) is invoked after each date so HTTP handlers can
// surface live progress; CLI passes nil and reads from the return
// value at the end.
//
// Same safety contract as the original cmd loop: dates outside the
// validated YYYY-MM-DD format produce a startup error rather than a
// runaway lex-compare loop (validation up front).
// dryRun=true skips the UpsertEnergySnapshotAt call but still counts OK
// — used by cmd/energy_backfill --dry-run for distribution sanity-checks
// against production data without mutating it.
func (s *DB) BackfillEnergyRange(
	ctx context.Context,
	tz, from, to string,
	dryRun bool,
	onProgress func(EnergyBackfillProgress),
) (EnergyBackfillProgress, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return EnergyBackfillProgress{}, fmt.Errorf("load tz %q: %w", tz, err)
	}
	if _, err := time.Parse("2006-01-02", from); err != nil {
		return EnergyBackfillProgress{}, fmt.Errorf("parse from %q: %w", from, err)
	}
	if _, err := time.Parse("2006-01-02", to); err != nil {
		return EnergyBackfillProgress{}, fmt.Errorf("parse to %q: %w", to, err)
	}
	if from > to {
		return EnergyBackfillProgress{}, fmt.Errorf("from %s is after to %s", from, to)
	}

	total, err := daysInRange(from, to)
	if err != nil {
		return EnergyBackfillProgress{}, err
	}
	p := EnergyBackfillProgress{From: from, To: to, TZ: tz, Total: total}

	for d := from; d <= to; {
		res, err := s.ComputeBankForDate(ctx, tz, d)
		if err != nil {
			log.Printf("[ENERGY_BACKFILL] %s compute: %v", d, err)
			p.Errors++
		} else if res.State == "stale" {
			p.Skipped++
		} else {
			res.Flags = append(res.Flags, "backfilled")
			ts, parseErr := time.ParseInLocation("2006-01-02 15:04", d+" 23:55", loc)
			if parseErr != nil {
				log.Printf("[ENERGY_BACKFILL] %s build EOD ts: %v", d, parseErr)
				p.Errors++
			} else if dryRun {
				p.OK++
			} else if err := s.UpsertEnergySnapshotAt(ctx, tz, ts, res); err != nil {
				log.Printf("[ENERGY_BACKFILL] %s write: %v", d, err)
				p.Errors++
			} else {
				p.OK++
			}
		}

		p.Done++
		if onProgress != nil {
			onProgress(p)
		}

		next, err := addDay(d)
		if err != nil {
			// Should never happen — we validated `from` above and addDay is
			// deterministic. Bail loud to avoid the runaway-loop class of
			// bugs caught on PR #46 (lex-compare loop iterating from year
			// 0001).
			return p, fmt.Errorf("advance date %q: %w", d, err)
		}
		d = next
	}

	return p, nil
}

// daysInRange returns the inclusive day count between two YYYY-MM-DD
// dates. Used to populate Progress.Total up front so progress bars
// can render a proper width from the first tick.
func daysInRange(from, to string) (int, error) {
	f, err := time.Parse("2006-01-02", from)
	if err != nil {
		return 0, err
	}
	t, err := time.Parse("2006-01-02", to)
	if err != nil {
		return 0, err
	}
	return int(t.Sub(f).Hours()/24) + 1, nil
}

// addDay returns d+1 day in YYYY-MM-DD form. Exported lowercase
// because cmd/energy_backfill also uses it for its own loop.
func addDay(d string) (string, error) {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return "", err
	}
	return t.AddDate(0, 0, 1).Format("2006-01-02"), nil
}

// ResolveBackfillDateRange picks defaults for from/to and validates
// user-provided values, matching the cmd's resolveDateRange exactly.
// Lifted here so the HTTP handler (PR a-ux1) gets the same defaults
// the CLI does — earliest complete daily_score for from, yesterday
// in tenant TZ for to.
func (s *DB) ResolveBackfillDateRange(ctx context.Context, tz, from, to string) (string, string, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return "", "", fmt.Errorf("load tz %q: %w", tz, err)
	}
	if to == "" {
		to = time.Now().In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	} else if _, err := time.Parse("2006-01-02", to); err != nil {
		return "", "", fmt.Errorf("to %q: must be YYYY-MM-DD: %w", to, err)
	}
	if from == "" {
		earliest, err := s.EarliestCompleteDailyScore(ctx)
		if err != nil {
			return "", "", err
		}
		from = earliest
	} else if _, err := time.Parse("2006-01-02", from); err != nil {
		return "", "", fmt.Errorf("from %q: must be YYYY-MM-DD: %w", from, err)
	}
	if from != "" && from > to {
		return "", "", fmt.Errorf("from %s is after to %s", from, to)
	}
	return from, to, nil
}

