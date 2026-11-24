package servingarchive

import (
	"os"
	"strconv"
	"testing"

	"health-receiver/internal/storage"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The worker smoke harness runs this binary inside its disposable PostgreSQL
// container, using the same restricted tenant role for each generation.
func TestHDBTWorkerSmokeStepChart(t *testing.T) {
	wantText := os.Getenv("HDBT_SMOKE_EXPECT")
	if wantText == "" {
		t.Skip("requires synthetic worker smoke fixture")
	}
	want, err := strconv.Atoi(wantText)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), "host=/var/run/postgresql dbname=postgres search_path=test_tenant,pg_catalog datestyle='German, DMY'")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	points, err := storage.NewFromPool(pool).DailyStepSeries(t.Context(), "", "2026-01-01")
	if err != nil || len(points) != 1 || points[0] != (storage.DailyStepPoint{Date: "2026-01-01", Qty: float64(want)}) {
		t.Fatalf("synthetic Steps All: got %+v, want %d: %v", points, want, err)
	}
}

// Bootstrap a clean schema using the same offline migration as tenant provisioning.
func TestHDBTWorkerSmokeProvisionCatalog(t *testing.T) {
	idText := os.Getenv("HDBT_SMOKE_CATALOG_TENANT")
	if idText == "" {
		t.Skip("requires synthetic worker smoke fixture")
	}
	id, err := uuid.Parse(idText)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), "host=/var/run/postgresql dbname=worker_catalog_fixture search_path=test_tenant,pg_catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := storage.NewFromPool(pool)
	if err := db.EnsureTenantIdentity(t.Context(), id, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateSchemaContractContext(t.Context()); err != nil {
		t.Fatal(err)
	}
}
