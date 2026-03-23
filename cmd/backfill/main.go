// cmd/backfill recomputes and caches readiness scores for all dates in the DB.
//
// Usage:
//
//	DATABASE_URL=postgres://... go run ./cmd/backfill           # incremental
//	DATABASE_URL=postgres://... go run ./cmd/backfill --force   # full rebuild
package main

import (
	"context"
	"log"
	"os"

	"health-receiver/internal/storage"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}

	force := false
	for _, arg := range os.Args[1:] {
		if arg == "--force" || arg == "-f" {
			force = true
		}
	}

	db, err := storage.New(context.Background(), dbURL)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Level 1+2: metric_points → minute_metrics → hourly_metrics (cascade)
	if err := db.BackfillAggregates(force); err != nil {
		log.Fatalf("backfill aggregates: %v", err)
	}

	// Level 3: readiness scores (reads from metric_points via sliding window)
	if err := db.BackfillScores(force); err != nil {
		log.Fatalf("backfill scores: %v", err)
	}
}
