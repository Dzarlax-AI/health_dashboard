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
//	  [--dry-run]
//
// Defaults:
//   --tz     = REPORT_TZ env var, falling back to UTC. Bad TZ fails loud
//              (off-by-one-day on midnight boundaries is the kind of bug
//              you don't notice until calibration breaks).
//   --from   = earliest date in daily_scores with complete inputs.
//   --to     = yesterday in --tz. Today is left to the live orchestrator
//              so this cmd never clobbers 5-min intraday buckets.
//   --schema = empty (uses search_path from DATABASE_URL). Multi-tenant
//              installs pass an explicit schema per run.
//
// The cmd is idempotent: each date upserts on (ts_bucket) where the bucket
// is "23:55 local on that date". Re-running with the same range overwrites
// previously-backfilled rows. Live intraday snapshots (5-min buckets
// throughout the day) live in different buckets and are never touched.
//
// All backfilled rows are flagged "backfilled" — calibration queries
// should filter on that flag to keep retrospective vs live data separable.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

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
	flag.Parse()

	loc, err := time.LoadLocation(*tzFlag)
	if err != nil {
		log.Fatalf("load tz %q: %v", *tzFlag, err)
	}

	ctx := context.Background()
	var db *storage.DB
	if *schemaFlag != "" {
		db, err = storage.NewWithSchema(ctx, dbURL, *schemaFlag)
	} else {
		db, err = storage.New(ctx, dbURL)
	}
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Make sure the destination table exists before we start computing —
	// fresh installs that have never run the live orchestrator wouldn't
	// have it yet.
	db.EnsureEnergySnapshotsTable()

	from, to, err := resolveDateRange(ctx, db, loc, *fromFlag, *toFlag)
	if err != nil {
		log.Fatalf("resolve date range: %v", err)
	}
	if from == "" {
		log.Println("no daily_scores rows with complete inputs — nothing to backfill")
		return
	}
	log.Printf("backfill window: %s → %s (tz=%s, schema=%q, dry-run=%v)",
		from, to, *tzFlag, *schemaFlag, *dryRun)

	var ok, skipped, errs int
	for d := from; d <= to; {
		res, err := db.ComputeBankForDate(ctx, *tzFlag, d)
		if err != nil {
			log.Printf("  %s: compute: %v", d, err)
			errs++
		} else if res.State == "stale" {
			// Insufficient inputs in the 21-day window leading up to
			// `d`. Common near the start of the import; the first ~14
			// days of any user's history are unavoidably stale because
			// the iteration needs lookback that doesn't exist yet.
			skipped++
		} else {
			// Mark before write so calibration queries can filter cleanly.
			res.Flags = append(res.Flags, "backfilled")

			// EOD bucket in tenant TZ. 23:55 (not 23:59) keeps a small buffer
			// from midnight rollover and stays within the date's 5-min grid.
			// resolveDateRange has already validated `d` parses as a date,
			// so ParseInLocation cannot fail here on malformed input — but
			// we still surface the error to catch DST-transition edge cases
			// where the constructed local time doesn't exist (e.g. 02:30
			// during a forward DST jump; 23:55 is well outside that window
			// but the check is cheap and future-proofs the buffer choice).
			ts, parseErr := time.ParseInLocation("2006-01-02 15:04", d+" 23:55", loc)
			if parseErr != nil {
				log.Printf("  %s: build EOD timestamp: %v", d, parseErr)
				errs++
			} else if *dryRun {
				log.Printf("  %s: bank=%d state=%s drain=%d restore=%d flags=%v (dry-run)",
					d, res.Bank, res.State, res.TodayDrain, res.TodayRestore, res.Flags)
				ok++
			} else if err := db.UpsertEnergySnapshotAt(ctx, *tzFlag, ts, res); err != nil {
				log.Printf("  %s: write: %v", d, err)
				errs++
			} else {
				ok++
			}
		}

		// Advance via real date arithmetic. addDay returns an error rather
		// than swallowing time.Parse failures because a bad `d` here would
		// silently fall back to year 0001 and the lexicographic loop
		// guard `d <= to` would then iterate ~700k times against the DB.
		// resolveDateRange validates user input on entry, so the only way
		// to reach this branch is a bug — log and stop.
		next, err := addDay(d)
		if err != nil {
			log.Fatalf("advance date %q: %v (internal invariant violated; aborting to avoid runaway loop)", d, err)
		}
		d = next
	}
	log.Printf("done: ok=%d skipped=%d errs=%d", ok, skipped, errs)
	// Surface partial failures as a non-zero exit so CI / cron / ops
	// automation can detect them. Summary line stays at log.Printf level
	// (not Fatal) so the structured ok/skipped/errs counts are still the
	// last thing in stdout — useful for parsing by wrappers.
	if errs > 0 {
		os.Exit(1)
	}
}

// resolveDateRange picks defaults for --from and --to and validates
// user-provided values. Empty --from returns ("", to, nil) when
// daily_scores has no complete rows; main treats that as "nothing to do".
//
// Validation is critical here, not optional: the iteration loop advances
// via string-formatted dates and uses a lexicographic `d <= to` guard.
// A malformed user input (e.g. `--from oct31` or `--from 2024/01/01`)
// that slipped through would parse as Go's zero time (year 0001) inside
// addDay, and "0001-01-02" <= "2026-05-11" is true — the loop would
// then iterate every day from year 1 to the target year, ~700k DB
// queries. Reject up front.
func resolveDateRange(ctx context.Context, db *storage.DB, loc *time.Location, from, to string) (string, string, error) {
	if to == "" {
		// Yesterday in tenant TZ — today is the live orchestrator's
		// domain. Clobbering today's intraday buckets with one synthetic
		// 23:55 row would erase the user's live drain curve.
		to = time.Now().In(loc).AddDate(0, 0, -1).Format("2006-01-02")
	} else if _, err := time.Parse("2006-01-02", to); err != nil {
		return "", "", fmt.Errorf("--to %q: must be YYYY-MM-DD: %w", to, err)
	}
	if from == "" {
		earliest, err := db.EarliestCompleteDailyScore(ctx)
		if err != nil {
			return "", "", err
		}
		from = earliest
	} else if _, err := time.Parse("2006-01-02", from); err != nil {
		return "", "", fmt.Errorf("--from %q: must be YYYY-MM-DD: %w", from, err)
	}
	if from != "" && from > to {
		return "", "", fmt.Errorf("--from %s is after --to %s", from, to)
	}
	return from, to, nil
}

func addDay(d string) (string, error) {
	t, err := time.Parse("2006-01-02", d)
	if err != nil {
		return "", err
	}
	return t.AddDate(0, 0, 1).Format("2006-01-02"), nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
