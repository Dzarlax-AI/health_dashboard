package storage

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"
)

// StressBackfillProgress mirrors EnergyBackfillProgress for the
// per-date sustained_hr_load + stress_flags recompute pass. Same
// shape so cmd/energy_backfill can render both in the same trace
// format, and a future HTTP handler can poll either with a uniform
// JSON contract.
type StressBackfillProgress struct {
	From    string `json:"from"`
	To      string `json:"to"`
	TZ      string `json:"tz"`
	Total   int    `json:"total"`
	Done    int    `json:"done"`
	OK      int    `json:"ok"`
	Skipped int    `json:"skipped"`
	Errors  int    `json:"errors"`
}

// BackfillStressRange recomputes daily_scores.sustained_hr_load and
// daily_scores.stress_flags for every date in [from, to]. Designed
// to run BEFORE BackfillEnergyRange so the bank's drain math sees
// the freshest sustained_hr_load values rather than whatever the
// last ingest happened to leave behind.
//
// dryRun=true logs the would-be writes without touching the DB —
// useful when the operator wants distribution stats from the
// existing data without disturbing it.
//
// Skipped vs Errors:
//   - Skipped: ComputeSustainedHRLoadForDate returned ok=true with
//     a stale_stress / calibration_warmup gate (the day is "honestly
//     missing", not a bug — the column should still be written with
//     gate flags, but the load itself is 0).
//   - Errors: ok=false (unparseable date, hard DB error) — row left
//     untouched.
//
// Date-format validation matches BackfillEnergyRange (up front, fail
// fast) so a typo can't trigger a runaway lex-compare loop.
func (s *DB) BackfillStressRange(
	ctx context.Context,
	tz, from, to string,
	dryRun bool,
	onProgress func(StressBackfillProgress),
) (StressBackfillProgress, error) {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return StressBackfillProgress{}, fmt.Errorf("load tz %q: %w", tz, err)
	}
	if _, err := time.Parse("2006-01-02", from); err != nil {
		return StressBackfillProgress{}, fmt.Errorf("parse from %q: %w", from, err)
	}
	if _, err := time.Parse("2006-01-02", to); err != nil {
		return StressBackfillProgress{}, fmt.Errorf("parse to %q: %w", to, err)
	}
	if from > to {
		return StressBackfillProgress{}, fmt.Errorf("from %s is after to %s", from, to)
	}

	total, err := daysInRange(from, to)
	if err != nil {
		return StressBackfillProgress{}, err
	}
	p := StressBackfillProgress{From: from, To: to, TZ: tz, Total: total}

	// classifyResult applies the same OK / Skipped gate to both
	// dry-run and production paths so the tallies the CLI prints
	// agree across modes. "Skipped" = the day computed cleanly but
	// a GATING flag (stale_stress / calibration_warmup) fired AND
	// the load itself ended up zero. Multi-channel diagnostic flags
	// (illness_signature, recovery_debt, parasympathetic_rebound,
	// acute_stress, sustained_load) don't gate the integral — those
	// days are valid OK even when the bank-side load is zero.
	classifyResult := func(res SustainedHRLoadResult) string {
		if res.SustainedHRLoadZ != 0 {
			return "ok"
		}
		for _, f := range res.Flags {
			if f == "stale_stress" || f == "calibration_warmup" || f == "data_accruing" {
				return "skipped"
			}
		}
		return "ok"
	}

	for d := from; d <= to; {
		// Honour caller cancellation so long-running backfills (650+
		// days across multiple tenants) can be aborted via context
		// timeout / signal handler. Returns current progress so
		// partial work is surfaced to the operator.
		select {
		case <-ctx.Done():
			return p, ctx.Err()
		default:
		}

		res, ok := s.ComputeSustainedHRLoadForDate(d, loc)
		switch {
		case !ok:
			// Compute-failure (unparseable date). The up-front
			// validation already caught the from/to boundaries, so
			// this only fires if a per-date subroutine somehow
			// rejected its input — surface as an error.
			p.Errors++
		case !dryRun:
			if err := s.writeSustainedHRLoadRow(d, res); err != nil {
				p.Errors++
			} else if classifyResult(res) == "skipped" {
				p.Skipped++
			} else {
				p.OK++
			}
		default:
			// Dry-run: classify the in-memory result without touching
			// the DB. Tallies agree with the live path because both
			// use classifyResult on the same `res` shape.
			if classifyResult(res) == "skipped" {
				p.Skipped++
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
			return p, fmt.Errorf("advance date %q: %w", d, err)
		}
		d = next
	}

	return p, nil
}

// StressDistributionStats summarises the sustained_hr_load + flag
// distribution over [from, to]. Used by cmd/energy_backfill
// --include-stress to print a one-shot calibration overview, and
// by the future PR-11 validation endpoint to seed Pearson r against
// HRV / sleep architecture / RHR channels.
//
// Percentiles use linear interpolation (Go-side, same convention as
// PostgreSQL percentile_cont) so the numbers in CLI stdout match
// what /api/admin/* will return.
type StressDistributionStats struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Days  int    `json:"days"`  // dates with sustained_hr_load IS NOT NULL
	Empty int    `json:"empty"` // dates with sustained_hr_load IS NULL

	// Load percentiles over non-null sustained_hr_load values.
	LoadMean   float64 `json:"load_mean"`
	LoadMedian float64 `json:"load_median"`
	LoadP90    float64 `json:"load_p90"`
	LoadMax    float64 `json:"load_max"`

	// Flag frequencies — count of dates where each flag fires.
	FlagCounts map[string]int `json:"flag_counts"`
}

// ComputeStressDistributionStats reads daily_scores for the date
// range and returns the summary above. Pure read; safe in dry-run.
func (s *DB) ComputeStressDistributionStats(
	ctx context.Context,
	from, to string,
) (StressDistributionStats, error) {
	if _, err := time.Parse("2006-01-02", from); err != nil {
		return StressDistributionStats{}, fmt.Errorf("parse from %q: %w", from, err)
	}
	if _, err := time.Parse("2006-01-02", to); err != nil {
		return StressDistributionStats{}, fmt.Errorf("parse to %q: %w", to, err)
	}
	// Reject inverted ranges the same way BackfillStressRange does
	// — otherwise a typo (from=05-10, to=05-01) silently returns an
	// empty-stats struct that looks like a "no data found" answer.
	if from > to {
		return StressDistributionStats{}, fmt.Errorf("from %s is after to %s", from, to)
	}

	rows, err := s.pool.Query(ctx, `
		SELECT sustained_hr_load, stress_flags
		  FROM daily_scores
		 WHERE date >= $1 AND date <= $2`, from, to)
	if err != nil {
		return StressDistributionStats{}, fmt.Errorf("query daily_scores: %w", err)
	}
	defer rows.Close()

	var loads []float64
	flagCounts := map[string]int{}
	var empty int
	for rows.Next() {
		var load *float64
		var flags []string
		if err := rows.Scan(&load, &flags); err != nil {
			return StressDistributionStats{}, fmt.Errorf("scan: %w", err)
		}
		if load == nil || math.IsNaN(*load) || math.IsInf(*load, 0) {
			empty++
		} else {
			loads = append(loads, *load)
		}
		for _, f := range flags {
			flagCounts[f]++
		}
	}
	if err := rows.Err(); err != nil {
		return StressDistributionStats{}, fmt.Errorf("rows.Err: %w", err)
	}

	res := StressDistributionStats{
		From:       from,
		To:         to,
		Days:       len(loads),
		Empty:      empty,
		FlagCounts: flagCounts,
	}
	if len(loads) > 0 {
		var sum float64
		for _, v := range loads {
			sum += v
		}
		res.LoadMean = sum / float64(len(loads))
		sort.Float64s(loads)
		res.LoadMedian = percentileSorted(loads, 0.5)
		res.LoadP90 = percentileSorted(loads, 0.9)
		res.LoadMax = loads[len(loads)-1]
	}
	return res, nil
}
