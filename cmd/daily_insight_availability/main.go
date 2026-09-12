// daily_insight_availability emits the aggregate-only, canonical B0
// availability report required before a tenant may enable Today Insights B0.
// It never reads raw health_records, calls a provider, or writes to the DB.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"regexp"
	"time"

	"health-receiver/internal/storage"
)

func main() {
	through := flag.String("through", "", "tenant-local through date (YYYY-MM-DD)")
	days := flag.Int("days", 108, "number of tenant-local days to evaluate (1..366)")
	schema := flag.String("schema", "health", "tenant schema to read")
	flag.Parse()
	if *through == "" {
		log.Fatal("--through is required")
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9_]*$`).MatchString(*schema) {
		log.Fatal("--schema must be a lowercase PostgreSQL identifier")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := storage.NewWithSchema(ctx, os.Getenv("DATABASE_URL"), *schema)
	if err != nil {
		log.Fatalf("open canonical report source: %v", err)
	}
	defer db.Close()
	report, err := db.RecentSleepAvailabilityReport(ctx, *through, *days, time.Now())
	if err != nil {
		log.Fatalf("build canonical availability report: %v", err)
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		log.Fatalf("encode availability report: %v", err)
	}
	fmt.Println(string(encoded))
}
