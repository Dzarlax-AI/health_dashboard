// cmd/import imports an Apple Health export (zip or xml) into the health database.
//
// Usage:
//
//	import --db /app/data/health.db --file export.zip [--batch 500] [--pause 200ms] [--dry-run]
//
// The importer streams the XML so it never loads the full file into memory.
// Batches are inserted with a configurable pause between them so the running
// server is not starved of DB connections during a large historical import.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"health-receiver/internal/applehealth"
	"health-receiver/internal/storage"
)

func main() {
	filePath := flag.String("file", "", "Apple Health export (.zip or export.xml) — required")
	batchSize := flag.Int("batch", 500, "metric points per DB transaction")
	pauseDur := flag.Duration("pause", 150*time.Millisecond, "sleep between batches (rate-limits DB load)")
	dryRun := flag.Bool("dry-run", false, "parse only — do not write to DB")
	flag.Parse()

	if *filePath == "" {
		log.Fatal("--file is required")
	}

	var db *storage.DB
	if !*dryRun {
		dbURL := os.Getenv("DATABASE_URL")
		if dbURL == "" {
			log.Fatal("DATABASE_URL environment variable is required")
		}
		var err error
		db, err = storage.New(context.Background(), dbURL)
		if err != nil {
			log.Fatalf("open db: %v", err)
		}
		defer db.Close()
	}

	var (
		totalParsed   int
		totalInserted int
		batchN        int
		pending       []storage.MetricPoint
		startTime     = time.Now()
	)

	flush := func(pts []storage.MetricPoint) {
		batchN++
		totalParsed += len(pts)

		if *dryRun {
			if totalParsed%100_000 == 0 || totalParsed < 1000 {
				log.Printf("[dry-run] parsed %d points so far…", totalParsed)
			}
			return
		}

		desc := fmt.Sprintf("apple-health-import batch %d", batchN)
		n, err := db.BulkInsertPoints(desc, pts)
		if err != nil {
			log.Printf("batch %d insert error: %v (continuing)", batchN, err)
		}
		totalInserted += n

		elapsed := time.Since(startTime).Round(time.Second)
		log.Printf("batch %d: %d parsed / %d new (total %d inserted, %s elapsed)",
			batchN, len(pts), n, totalInserted, elapsed)

		if *pauseDur > 0 {
			time.Sleep(*pauseDur)
		}
	}

	// Collect points up to batchSize, then flush.
	emit := func(pts []storage.MetricPoint) {
		pending = append(pending, pts...)
		for len(pending) >= *batchSize {
			flush(pending[:*batchSize])
			pending = pending[*batchSize:]
		}
	}

	log.Printf("starting import from %s (batch=%d pause=%s dry-run=%v)",
		*filePath, *batchSize, *pauseDur, *dryRun)

	var parseErr error
	switch {
	case len(*filePath) > 4 && (*filePath)[len(*filePath)-4:] == ".zip":
		parseErr = applehealth.ParseZip(*filePath, emit, nil)
	default:
		parseErr = applehealth.ParseXMLFile(*filePath, emit, nil)
	}
	if len(pending) > 0 {
		flush(pending)
	}

	if parseErr != nil {
		log.Printf("parse error (partial import may have succeeded): %v", parseErr)
	}

	elapsed := time.Since(startTime).Round(time.Second)
	if *dryRun {
		log.Printf("dry-run complete: %d points parsed in %s", totalParsed, elapsed)
		return
	}
	log.Printf("import complete: %d points parsed, %d inserted, %d duplicates skipped in %s",
		totalParsed, totalInserted, totalParsed-totalInserted, elapsed)

	// Trigger a full backfill so daily_scores and caches are up to date.
	log.Println("running backfill (this may take a few minutes)…")
	db.BackfillAggregates(true)
	db.BackfillScores(true)
	log.Println("backfill done — import complete")
}
