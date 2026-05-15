// cmd/migrate_sleep_unspecified moves historical sleep_core rows that have
// no sibling sleep_deep/sleep_rem (same source, within ±1 calendar day) to
// the sleep_unspecified metric. Those rows are pure coarse-asleep data from
// sources without stage tracking (RingConn, iPhone Sleep Schedule, older
// Apple Watch); they should never have been called "core" in the first
// place.
//
// Usage:
//
//	DATABASE_URL=postgres://...?search_path=health \
//	  go run ./cmd/migrate_sleep_unspecified --dry-run   # count, no writes
//	DATABASE_URL=...                                    \
//	  go run ./cmd/migrate_sleep_unspecified            # apply + targeted rebuild
//
// Idempotent: UPDATE only touches sleep_core rows still matching the
// "no stage siblings in ±1 day" predicate. Second run finds zero candidates.
//
// **Targeted rebuild:** after the UPDATE the script collects the distinct
// dates touched (`SELECT DISTINCT SUBSTRING(date,1,10) ...`) and calls
// `storage.DB.UpsertRecentCache` for those dates only. That per-date path
// rebuilds the affected `hourly_metrics` rows from `metric_points` and
// then the `daily_scores` row via the same atomic sleep block the server
// uses on `/health` ingest. The previous version of this script told the
// operator to run `make backfill --force` afterwards, which triggers a
// full hourly+daily rebuild across the entire history (hours of work for
// ~10k rows of changes). The targeted path completes in seconds per date.
//
// `recomputeReadiness=false`: the migration changes `metric_name` from
// `sleep_core` to `sleep_unspecified` but does not change the underlying
// duration. `sleep_total` for affected nights is unchanged (it is summed
// from segments under multiple stage names), so the readiness score
// (HRV × RHR × sleep) does not need recomputation.
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/jackc/pgx/v5"

	"health-receiver/internal/storage"
)

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

// Collect dates after the UPDATE so we know exactly which days need
// their hourly_metrics + daily_scores rebuilt. Reading from the freshly
// migrated rows is cheaper than re-evaluating the predicate.
const affectedDatesSQL = `
	SELECT DISTINCT SUBSTRING(date,1,10) AS day
	FROM metric_points
	WHERE metric_name = 'sleep_unspecified'
	ORDER BY day`

func main() {
	dryRun := flag.Bool("dry-run", false, "count candidates without writing")
	flag.Parse()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL is required")
	}

	ctx := context.Background()

	// Two connections: a bare pgx for the UPDATE (single-statement SQL,
	// no need for the storage abstraction), and a storage.DB for the
	// targeted UpsertRecentCache afterwards. Both honour `search_path`
	// from the DSN, so multi-tenant installs point this at one schema
	// per run.
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

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
	// window when the row exists under both names (which would double-
	// count if a /health POST landed mid-migration).
	tag, err := conn.Exec(ctx, updateSQL)
	if err != nil {
		log.Fatalf("update: %v", err)
	}
	log.Printf("migrated %d rows", tag.RowsAffected())

	rows, err := conn.Query(ctx, affectedDatesSQL)
	if err != nil {
		log.Fatalf("collect affected dates: %v", err)
	}
	var dates []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			rows.Close()
			log.Fatalf("scan affected date: %v", err)
		}
		dates = append(dates, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		log.Fatalf("iterate affected dates: %v", err)
	}
	log.Printf("rebuilding cache for %d affected dates", len(dates))

	// Clear stale sleep_* rows from hourly_metrics for affected dates
	// before the targeted rebuild. The rebuild path
	// (upsertHourlySleepForDate) uses INSERT ... ON CONFLICT DO UPDATE,
	// which only touches rows for metric_names that still exist in
	// metric_points; it never removes a row whose metric_name has
	// disappeared. After the UPDATE above, source S's old sleep_core
	// hourly row would survive next to the new sleep_unspecified row,
	// leaving per_source with three sleep_* metric_names — neither the
	// 5-stage branch nor the (total + unspecified) branch of
	// sleep_picked_complete fires, the atomicity gate fails, and
	// daily_scores.sleep_unspecified ends up NULL via COALESCE preserve.
	// Flagged by Codex on PR #75 — the original implementation only
	// worked because the operator was told to run `make backfill --force`,
	// which truncates hourly_metrics outright.
	//
	// Scope is per-DATE not per-(date, source): UpsertRecentCache rebuilds
	// every source's sleep rows for that date from metric_points anyway,
	// so deleting them upfront is free; per-source filtering would just
	// be more SQL for no benefit.
	tag2, err := conn.Exec(ctx, `
		DELETE FROM hourly_metrics
		 WHERE metric_name LIKE 'sleep\_%' ESCAPE '\'
		   AND SUBSTRING(hour,1,10) = ANY($1::text[])`, dates)
	if err != nil {
		log.Fatalf("clear stale hourly sleep rows: %v", err)
	}
	log.Printf("cleared %d stale hourly_metrics sleep rows", tag2.RowsAffected())

	// storage.DB so we can call the same per-date cache rebuild path the
	// server uses on every /health POST. This is orders of magnitude
	// faster than `cmd/backfill --force` which DELETEs all of
	// hourly_metrics and rebuilds the whole history from scratch.
	db, err := storage.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("storage.New: %v", err)
	}
	defer db.Close()

	db.UpsertRecentCache(dates, false)
	log.Printf("rebuilt %d dates; done", len(dates))
}
