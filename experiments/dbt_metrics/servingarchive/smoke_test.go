package servingarchive

import (
	"os"
	"strconv"
	"testing"

	"health-receiver/internal/storage"

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
