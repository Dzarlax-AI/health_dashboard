package main

import (
	"context"
	"strings"
	"testing"
)

func TestOpenAvailabilitySourceRequiresDirectDSNWhenIsolationDisabled(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("TENANT_DB_ISOLATION_ENABLED", "false")
	for _, key := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER"} {
		t.Setenv(key, "")
	}

	db, closeSource, err := openAvailabilitySource(context.Background(), "health")
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL is required") {
		t.Fatalf("openAvailabilitySource error = %v, want missing direct DSN error", err)
	}
	if db != nil || closeSource != nil {
		t.Fatalf("openAvailabilitySource returned source on configuration error")
	}
}

func TestOpenAvailabilitySourceRejectsIncompleteIsolationConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	t.Setenv("TENANT_DB_ISOLATION_ENABLED", "true")
	t.Setenv("ADMIN_DATABASE_URL", "")
	t.Setenv("REGISTRY_DATABASE_URL", "")
	t.Setenv("TENANT_DATABASE_URL_BASE", "")
	t.Setenv("TENANT_DB_MASTER_SECRET", "")
	t.Setenv("TENANT_DB_MASTER_SECRET_VERSION", "")
	for _, key := range []string{"PGHOST", "PGPORT", "PGDATABASE", "PGUSER"} {
		t.Setenv(key, "")
	}

	db, closeSource, err := openAvailabilitySource(context.Background(), "health")
	if err == nil || !strings.Contains(err.Error(), "parse isolated tenant source") {
		t.Fatalf("openAvailabilitySource error = %v, want isolated config error", err)
	}
	if db != nil || closeSource != nil {
		t.Fatalf("openAvailabilitySource returned source on configuration error")
	}
}
