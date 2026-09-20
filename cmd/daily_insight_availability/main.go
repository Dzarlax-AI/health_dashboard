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
	"strings"
	"time"

	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
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
	db, closeSource, err := openAvailabilitySource(ctx, *schema)
	if err != nil {
		log.Fatalf("open canonical report source: %v", err)
	}
	defer closeSource()
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

// openAvailabilitySource uses a direct read-only DSN when one is deliberately
// supplied. Production isolated tenants do not expose such a DSN: in that
// mode it opens the same derived, schema-bound tenant pool as the service.
// Neither path writes data or falls back to an administrative tenant pool.
func openAvailabilitySource(ctx context.Context, schema string) (*storage.DB, func(), error) {
	cfg, err := tenants.ParseTenantIsolationConfig(os.LookupEnv)
	if err != nil {
		return nil, nil, fmt.Errorf("parse isolated tenant source: %w", err)
	}
	if cfg.Enabled {
		db, closeSource, err := tenants.OpenReadOnlyTenant(ctx, cfg, schema)
		if err != nil {
			return nil, nil, fmt.Errorf("open isolated tenant source: %w", err)
		}
		return db, closeSource, nil
	}

	dsn := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if dsn != "" || standardPostgresEnvConfigured() {
		db, err := storage.NewWithSchema(ctx, dsn, schema)
		if err != nil {
			return nil, nil, err
		}
		return db, db.Close, nil
	}

	return nil, nil, fmt.Errorf("DATABASE_URL is required when tenant database isolation is disabled")
}

func standardPostgresEnvConfigured() bool {
	for _, key := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER"} {
		if strings.TrimSpace(os.Getenv(key)) != "" {
			return true
		}
	}
	return false
}
