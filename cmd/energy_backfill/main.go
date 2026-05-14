// cmd/energy_backfill replays the EnergyBank v2 formula over historical
// daily_scores and writes one EOD snapshot per date. Intended as a
// one-shot to seed energy_snapshots with retrospective data — useful
// before the v1→v2 verdict-threshold cutover (when ≥30 real-world EOD
// points are needed to set rest / recovery / push_hard bands honestly),
// and for any new install that already has months of Apple Health
// import behind it.
//
// Usage:
//
//	DATABASE_URL=postgres://... go run ./cmd/energy_backfill \
//	  --tz Europe/Belgrade \
//	  --from 2024-07-01 \
//	  --to 2026-05-11 \
//	  [--schema health] \
//	  [--include-stress] \
//	  [--dry-run]
//
// Defaults:
//   --tz             = REPORT_TZ env var, falling back to UTC.
//   --from           = earliest date in daily_scores with complete inputs.
//   --to             = yesterday in --tz. Today is left to the live orchestrator.
//   --schema         = empty (uses search_path from DATABASE_URL).
//   --include-stress = false. When set, runs a sustained_hr_load + stress_flags
//                      recompute pass BEFORE the bank backfill so the v2.2 β
//                      term consumes fresh values. Also prints a distribution
//                      summary (mean/median/p90/max + flag counts) for the
//                      same window — useful for the §4.5 calibration review
//                      before flipping settings.energy.stress_drain_enabled.
//
// The cmd is idempotent: re-running with the same range overwrites
// previously-backfilled rows. Live intraday snapshots live in
// different 5-min buckets and are never touched.
//
// The iteration loop, validation, and write logic all live in
// `internal/storage.(*DB).BackfillEnergyRange` so the per-user
// settings UI button (PR a-ux1) and the auto-trigger-after-import
// hook share the same code path. This cmd is a thin wrapper that
// surfaces a CLI surface, a dry-run mode, and an exit code wired to
// the partial-failure count.
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"sort"

	// Embed IANA tz data — same rationale as cmd/server, the alpine-style
	// build doesn't ship tzdata and a silent UTC fallback would mis-bucket
	// every snapshot.
	_ "time/tzdata"

	"health-receiver/internal/storage"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	tzFlag := flag.String("tz", envOr("REPORT_TZ", "UTC"), "Tenant timezone (e.g. Europe/Belgrade)")
	fromFlag := flag.String("from", "", "Start date YYYY-MM-DD (inclusive); empty = earliest complete day")
	toFlag := flag.String("to", "", "End date YYYY-MM-DD (inclusive); empty = yesterday in --tz")
	schemaFlag := flag.String("schema", "", "Tenant schema (multi-tenant installs); empty = use search_path from DATABASE_URL")
	dryRun := flag.Bool("dry-run", false, "Compute and log without writing")
	includeStress := flag.Bool("include-stress", false, "Recompute sustained_hr_load + stress_flags first, then print distribution stats")
	flag.Parse()

	ctx := context.Background()
	var (
		db  *storage.DB
		err error
	)
	if *schemaFlag != "" {
		db, err = storage.NewWithSchema(ctx, dbURL, *schemaFlag)
	} else {
		db, err = storage.New(ctx, dbURL)
	}
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	db.EnsureEnergySnapshotsTable()

	from, to, err := db.ResolveBackfillDateRange(ctx, *tzFlag, *fromFlag, *toFlag)
	if err != nil {
		log.Fatalf("resolve date range: %v", err)
	}
	if from == "" {
		log.Println("no daily_scores rows with complete inputs — nothing to backfill")
		return
	}
	log.Printf("backfill window: %s → %s (tz=%s, schema=%q, dry-run=%v, include-stress=%v)",
		from, to, *tzFlag, *schemaFlag, *dryRun, *includeStress)

	// Stress recompute runs BEFORE the bank backfill so the v2.2 β
	// term reads the freshest sustained_hr_load values. On dry-run
	// we still iterate (using the dry-run flag downstream) so the
	// distribution stats line up with what a live run would produce.
	if *includeStress {
		lastStress := -1
		sp, err := db.BackfillStressRange(ctx, *tzFlag, from, to, *dryRun, func(p storage.StressBackfillProgress) {
			if p.Done == lastStress {
				return
			}
			lastStress = p.Done
			if p.Done == p.Total || p.Done%50 == 0 {
				log.Printf("  [stress] %d/%d  ok=%d skipped=%d errs=%d", p.Done, p.Total, p.OK, p.Skipped, p.Errors)
			}
		})
		if err != nil {
			log.Fatalf("stress backfill: %v", err)
		}
		log.Printf("[stress] done: ok=%d skipped=%d errs=%d", sp.OK, sp.Skipped, sp.Errors)
	}

	// Per-date progress callback prints structured tally. Keeps the
	// CLI's existing "saw N dates, wrote M, skipped K, errored E"
	// trace for wrapper scripts that parse stdout.
	last := -1
	progress, err := db.BackfillEnergyRange(ctx, *tzFlag, from, to, *dryRun, func(p storage.EnergyBackfillProgress) {
		if p.Done == last {
			return
		}
		last = p.Done
		// Trace every 10th date plus the final to keep stdout readable
		// on long backfills (650+ days). Skipped/errored dates still
		// surface via the per-row log lines inside the function.
		if p.Done == p.Total || p.Done%10 == 0 {
			log.Printf("  %d/%d  ok=%d skipped=%d errs=%d", p.Done, p.Total, p.OK, p.Skipped, p.Errors)
		}
	})
	if err != nil {
		log.Fatalf("backfill: %v", err)
	}
	log.Printf("done: ok=%d skipped=%d errs=%d", progress.OK, progress.Skipped, progress.Errors)

	// Distribution overview — printed after the bank pass so the
	// sustained_hr_load column reflects the freshly-computed values
	// when --include-stress was set. Pure read; safe with --dry-run.
	if *includeStress {
		stats, statsErr := db.ComputeStressDistributionStats(ctx, from, to)
		if statsErr != nil {
			log.Printf("distribution stats: %v", statsErr)
		} else {
			log.Printf("[stress dist] window %s → %s  days=%d (%d empty)",
				stats.From, stats.To, stats.Days, stats.Empty)
			if stats.Days > 0 {
				log.Printf("[stress dist] sustained_hr_load_z  mean=%.2f  median=%.2f  p90=%.2f  max=%.2f",
					stats.LoadMean, stats.LoadMedian, stats.LoadP90, stats.LoadMax)
			}
			if len(stats.FlagCounts) > 0 {
				// Sort flag names for stable output (script-friendly).
				names := make([]string, 0, len(stats.FlagCounts))
				for k := range stats.FlagCounts {
					names = append(names, k)
				}
				sort.Strings(names)
				log.Println("[stress dist] flag frequencies:")
				for _, n := range names {
					log.Printf("    %-28s %d", n, stats.FlagCounts[n])
				}
			}
		}
	}
	// Surface partial failures as a non-zero exit so CI / cron / ops
	// automation can detect them. Summary line stays at log.Printf
	// level (not Fatal) so the structured ok/skipped/errs counts are
	// still the last thing in stdout — useful for parsing by wrappers.
	if progress.Errors > 0 {
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
