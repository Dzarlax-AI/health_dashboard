// Command wake_detection_backfill computes canonical historical wake_time
// metrics. It defaults to dry-run and requires --apply before writing.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	_ "time/tzdata"

	"health-receiver/internal/registry"
	"health-receiver/internal/storage"
	"health-receiver/internal/tenants"
)

func main() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL environment variable is required")
	}
	tzName := flag.String("tz", envOr("REPORT_TZ", "UTC"), "Tenant timezone")
	fromValue := flag.String("from", "", "Start date YYYY-MM-DD (required)")
	toValue := flag.String("to", "", "End date YYYY-MM-DD (required)")
	schema := flag.String("schema", "", "Tenant schema; empty uses DATABASE_URL search_path")
	apply := flag.Bool("apply", false, "Persist detected wake_time rows; default is dry-run")
	flag.Parse()
	if *fromValue == "" || *toValue == "" {
		log.Fatal("--from and --to are required")
	}
	loc, err := time.LoadLocation(*tzName)
	if err != nil {
		log.Fatalf("load timezone: %v", err)
	}
	from, err := time.ParseInLocation("2006-01-02", *fromValue, loc)
	if err != nil {
		log.Fatalf("parse --from: %v", err)
	}
	to, err := time.ParseInLocation("2006-01-02", *toValue, loc)
	if err != nil {
		log.Fatalf("parse --to: %v", err)
	}
	if from.After(to) {
		log.Fatal("--from must not be after --to")
	}

	ctx := context.Background()
	db, err := openTenantDB(ctx, dbURL, *schema)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer db.Close()
	result, err := db.BackfillWakeTimes(*fromValue, *toValue, loc, !*apply)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wake backfill dry_run=%t attempted=%d detected=%d missing=%d written=%d\n",
		!*apply, result.Attempted, result.Detected, result.Missing, result.Written)
}

func openTenantDB(ctx context.Context, dbURL, schema string) (*storage.DB, error) {
	isolation, err := tenants.ParseTenantIsolationConfig(os.LookupEnv)
	if err != nil {
		return nil, fmt.Errorf("parse tenant isolation config: %w", err)
	}
	if !isolation.Enabled {
		if schema == "" {
			return storage.New(ctx, dbURL)
		}
		return storage.NewWithSchema(ctx, dbURL, schema)
	}
	if schema == "" {
		return nil, fmt.Errorf("--schema is required when tenant database isolation is enabled")
	}
	reg, err := registry.New(ctx, isolation.RegistryDSN)
	if err != nil {
		return nil, fmt.Errorf("open registry: %w", err)
	}
	defer reg.Close()
	user, err := reg.GetBySchema(ctx, schema)
	if err != nil {
		return nil, fmt.Errorf("resolve tenant schema: %w", err)
	}
	password, err := isolation.Credentials.Derive(user.TenantID, user.DBRole, user.DBCredentialVersion)
	if err != nil {
		return nil, fmt.Errorf("derive restricted tenant credential: %w", err)
	}
	return storage.NewRestrictedTenant(ctx, isolation.TenantDSNBase, user.DBRole, password, user.SchemaName)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
