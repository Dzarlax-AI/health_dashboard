// cmd/migrate_sleep_unspecified moves historical sleep_core rows that have
// no sibling sleep_deep/sleep_rem (same day + same source) to the
// sleep_unspecified metric. Those rows are pure coarse-asleep data from
// sources without stage tracking (RingConn, iPhone Sleep Schedule, older
// Apple Watch); they should never have been called "core" in the first
// place.
//
// Usage:
//
//	DATABASE_URL=postgres://...?search_path=health \
//	  go run ./cmd/migrate_sleep_unspecified --dry-run   # count, no writes
//	DATABASE_URL=...                                    \
//	  go run ./cmd/migrate_sleep_unspecified            # apply + recompute
//
// Idempotent: UPDATE only touches sleep_core rows still matching the
// "no stage siblings" predicate. Second run is a no-op.
//
// Cache rebuild: drops affected daily_scores rows so the next /admin
// backfill (or `make backfill`) repopulates sleep_core / sleep_unspecified
// atomically via buildDailySleepBlock. We don't recompute inline to keep
// this script SQL-only and side-effect-bounded.
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/jackc/pgx/v5"
)

func main() {
	dryRun := flag.Bool("dry-run", false, "count candidates without writing")
	flag.Parse()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	// "coarse-only" predicate: same source has no stage-tracking rows
	// (sleep_deep / sleep_rem) within ±1 calendar day of this fragment.
	//
	// Why a window instead of strict same-day: HK fragments are stored
	// under their startDate, so a normal Apple Watch staged night that
	// crosses midnight can park a sleep_core fragment on day N (before
	// midnight) while the deep / REM fragments land on day N+1 (after
	// midnight). A strict same-day NOT EXISTS would mis-classify those
	// legitimate Watch core fragments as coarse and corrupt historical
	// stage data — flagged by CodeRabbit on PR #73 after a baseline
	// audit showed Apple Watch, Withings, RingConn, and Pillow all
	// emitting BOTH coarse-only and staged nights from the same source.
	//
	// ±1 day catches cross-midnight pairs and tolerates fragments
	// recorded with slightly skewed timestamps; sources that genuinely
	// never report stages (Zepp Life, SleepWatch, Sleep Cycle in this
	// install) still match the predicate cleanly. A row whose source
	// has any deep/REM fragment within the window stays put — better
	// to leave a handful of legit-coarse rows behind than to corrupt
	// real stage data on reimport.
	const candidateSQL = `
		SELECT COUNT(*), COUNT(DISTINCT source)
		FROM metric_points c
		WHERE c.metric_name = 'sleep_core'
		  AND NOT EXISTS (
			SELECT 1 FROM metric_points x
			WHERE x.source = c.source
			  AND x.metric_name IN ('sleep_deep','sleep_rem')
			  AND SUBSTRING(x.date,1,10)::date BETWEEN
			      SUBSTRING(c.date,1,10)::date - INTERVAL '1 day'
			  AND SUBSTRING(c.date,1,10)::date + INTERVAL '1 day'
		  )`

	var rowCount, sourceCount int
	if err := conn.QueryRow(ctx, candidateSQL).Scan(&rowCount, &sourceCount); err != nil {
		log.Fatalf("count candidates: %v", err)
	}
	log.Printf("candidates: %d sleep_core rows across %d sources", rowCount, sourceCount)
	if rowCount == 0 {
		log.Println("nothing to do")
		return
	}
	if *dryRun {
		log.Println("--dry-run set, no writes")
		return
	}

	// UPDATE in place is preferred over INSERT+DELETE: avoids the brief
	// window when the row exists under both names (which would
	// double-count if a /health POST landed mid-migration).
	const updateSQL = `
		UPDATE metric_points
		SET metric_name = 'sleep_unspecified'
		WHERE metric_name = 'sleep_core'
		  AND NOT EXISTS (
			SELECT 1 FROM metric_points x
			WHERE x.source = metric_points.source
			  AND x.metric_name IN ('sleep_deep','sleep_rem')
			  AND SUBSTRING(x.date,1,10)::date BETWEEN
			      SUBSTRING(metric_points.date,1,10)::date - INTERVAL '1 day'
			  AND SUBSTRING(metric_points.date,1,10)::date + INTERVAL '1 day'
		  )`
	tag, err := conn.Exec(ctx, updateSQL)
	if err != nil {
		log.Fatalf("update: %v", err)
	}
	log.Printf("migrated %d rows", tag.RowsAffected())

	// Force a cache rebuild for affected dates by clearing their daily
	// sleep columns. The next backfill (manual or scheduled) will rebuild
	// them through buildDailySleepBlock which knows about sleep_unspecified.
	const clearSQL = `
		UPDATE daily_scores SET sleep_core = NULL, sleep_unspecified = NULL
		WHERE date IN (
			SELECT DISTINCT SUBSTRING(date,1,10) FROM metric_points
			WHERE metric_name = 'sleep_unspecified'
		)`
	tag, err = conn.Exec(ctx, clearSQL)
	if err != nil {
		log.Fatalf("clear cache: %v", err)
	}
	log.Printf("cleared %d daily_scores rows (run `make backfill` to repopulate)", tag.RowsAffected())
}
